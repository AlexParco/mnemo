// Package selfupdate replaces the running mnemo with another release: find the
// version, download it, verify it, put it in place, and say what happened.
//
// Two things shape everything here. The first is that the file being replaced is
// the one that is executing, so the write has to be a rename and never a write
// in place — a binary overwritten in place is a half-binary that the kernel is
// still reading. The second is that an update that got as far as the disk cannot
// be undone by returning an error: once the new binary is in place, whatever
// fails afterwards is reported next to a success, not instead of it.
//
// Nothing here prints and nothing here decides: every outside thing it needs —
// the release index, the running version, restarting the server, updating the
// plugin — comes in through Options, so the package can be driven end to end by
// a test with no network and the command layer keeps the choices that belong to
// it.
package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// A Refusal is an answer, not a failure: something the caller can show a person
// as it stands. Anything else that goes wrong is a plain error and reads like a
// fault, because it is one.
type Refusal struct {
	Message string
}

func (r *Refusal) Error() string { return r.Message }

func refuse(format string, args ...any) error {
	return &Refusal{Message: fmt.Sprintf(format, args...)}
}

// IsRefusal reports whether an error is something to show rather than to fix.
func IsRefusal(err error) bool {
	var refusal *Refusal
	return errors.As(err, &refusal)
}

// Default addresses of the real release. They are options rather than constants
// in the code path so that a test can serve all three from httptest: an update
// that can only be exercised against GitHub is an update nobody tests.
const (
	// LatestRelease answers with the release index, whose tag_name is the latest
	// version.
	LatestRelease = "https://api.github.com/repos/AlexParco/mnemo/releases/latest"
	// Downloads is the directory the archives and checksums.txt hang under, one
	// subdirectory per tag.
	Downloads = "https://github.com/AlexParco/mnemo/releases/download"
)

// Options is everything Update needs from outside itself.
type Options struct {
	// Running is the version of this executable, which the command layer takes
	// from internal/build. Passed in rather than read here so that a test can
	// stand at any version it likes.
	Running string
	// Wanted is the version asked for by --version. Empty means the latest.
	Wanted string

	// Executable is the file to replace. Empty means the running one.
	Executable string

	// OS and Arch name the build to fetch, and with it the shape of the archive
	// and the way the file is replaced. Empty means this machine. A test sets
	// them to walk the Windows path on a Unix box, which is the only way that
	// path gets walked at all before a user walks it.
	OS, Arch string

	// LatestRelease and Downloads override the addresses above.
	LatestRelease string
	Downloads     string
	Client        *http.Client

	// RestartService restarts this machine's server service, and RestartTunnel
	// the tunnel keeper. Both are injected, and both are nil when there is
	// nothing to restart.
	//
	// They are injected because only internal/service and internal/tunnel can
	// answer whether this machine has a service at all or whether the keeper is
	// running, and that question is asked before the download, in the command
	// layer, where its answer can also be printed. Doing it here would also put
	// a process-management dependency inside the one package whose tests must
	// stay hermetic.
	RestartService func(context.Context) error
	RestartTunnel  func(context.Context) error

	// UpdatePlugin runs `claude plugin update mnemo@mnemo`, and is nil when the
	// plugin is not installed. Injected for the same reason: internal/integration
	// already owns the plugin's name and the question of whether Claude Code has
	// it, and this package may not import it.
	UpdatePlugin func(context.Context) error
}

// Result is what happened, for the caller to print.
type Result struct {
	// From and To are versions without the leading v, as `mnemo version` prints
	// them. They are equal when nothing was done.
	From string
	To   string
	// UpToDate is the whole outcome when the running version is the target:
	// nothing was downloaded and nothing was touched.
	UpToDate bool
	// Executable is the file that was replaced.
	Executable string
	// Restarted and PluginUpdated record the steps after the replacement, each
	// of which only ran when the caller gave the function for it.
	ServiceRestarted bool
	TunnelRestarted  bool
	PluginUpdated    bool
	// Trouble is what went wrong after the new binary was already in place. It
	// is not an error, because by then the update has happened and saying it
	// failed would be a lie; it is the list of things the person has to finish
	// by hand.
	Trouble []string
}

// Line is the one sentence the command prints, as docs/specs/release.md gives it.
func (r Result) Line() string {
	if r.UpToDate {
		return fmt.Sprintf("mnemo %s is up to date", r.From)
	}
	return fmt.Sprintf("Updated to %s. Open chats use the new version after they restart.", r.To)
}

// Update carries out the seven steps of `mnemo update`.
//
// The order is the contract's and it is not an accident: nothing is downloaded
// before the package-manager refusal, and nothing is written before the checksum
// matches, so every way of stopping early leaves the disk exactly as it was.
func Update(ctx context.Context, opts Options) (Result, error) {
	opts = opts.withDefaults()

	wanted, err := requestedVersion(opts.Wanted)
	if err != nil {
		return Result{}, err
	}
	running := plainVersion(opts.Running)
	if wanted == "" {
		latest, err := latestVersion(ctx, opts)
		if err != nil {
			return Result{}, err
		}
		wanted = latest
	}
	// A build from a working copy has no version to compare — "dev" is never
	// equal to a release — so it updates rather than claiming to be current.
	if wanted == running {
		return Result{From: running, To: running, UpToDate: true, Executable: opts.Executable}, nil
	}

	// The real file, resolved, is what both the refusal and the replacement work
	// on. Homebrew's entry on the PATH is a symlink into the Cellar, so testing
	// the link would miss every Homebrew install there is; and replacing a
	// symlink rather than following it would leave the old binary on disk and
	// turn a link somebody set up on purpose into a plain file.
	target := opts.Executable
	if resolved, err := filepath.EvalSymlinks(target); err == nil {
		target = resolved
	}
	if err := refusePackageManager(target); err != nil {
		return Result{}, err
	}

	archive, err := download(ctx, opts, wanted)
	if err != nil {
		return Result{}, err
	}
	binary, err := binaryIn(archive, opts.OS)
	if err != nil {
		return Result{}, err
	}
	if err := replace(target, binary, opts.OS); err != nil {
		return Result{}, err
	}

	result := Result{From: running, To: wanted, Executable: target}
	// Past this point the new binary is on disk. Everything that follows is
	// recorded as trouble and never as an error: a failed restart does not
	// un-replace the file, and a caller that saw an error here would tell the
	// person the update did not happen.
	if opts.RestartService != nil {
		if err := opts.RestartService(ctx); err != nil {
			result.Trouble = append(result.Trouble, fmt.Sprintf("The new mnemo is in place, but restarting the "+
				"server service failed: %v. Restart it with: mnemo server restart", err))
		} else {
			result.ServiceRestarted = true
		}
	}
	if opts.RestartTunnel != nil {
		if err := opts.RestartTunnel(ctx); err != nil {
			result.Trouble = append(result.Trouble, fmt.Sprintf("The new mnemo is in place, but restarting the "+
				"tunnel failed: %v. The server is still reachable on the local network", err))
		} else {
			result.TunnelRestarted = true
		}
	}
	if opts.UpdatePlugin != nil {
		if err := opts.UpdatePlugin(ctx); err != nil {
			result.Trouble = append(result.Trouble, fmt.Sprintf("The new mnemo is in place, but updating the "+
				"Claude Code plugin failed: %v. Update it with: claude plugin update mnemo@mnemo", err))
		} else {
			result.PluginUpdated = true
		}
	}
	return result, nil
}

func (o Options) withDefaults() Options {
	if o.Executable == "" {
		// An error here is left to travel: a machine that cannot name its own
		// executable has nothing for this package to replace, and the empty
		// string would mean the working directory.
		o.Executable, _ = os.Executable()
	}
	if o.OS == "" {
		o.OS = runtime.GOOS
	}
	if o.Arch == "" {
		o.Arch = runtime.GOARCH
	}
	if o.LatestRelease == "" {
		o.LatestRelease = LatestRelease
	}
	if o.Downloads == "" {
		o.Downloads = Downloads
	}
	if o.Client == nil {
		// A timeout rather than none: an update that hangs for ever on a dead
		// connection looks to the person like mnemo itself has hung.
		o.Client = &http.Client{Timeout: 5 * time.Minute}
	}
	return o
}

// version is the shape a tag may have: v0.6.0, with an optional pre-release.
// Checked rather than trusted because the value ends up in a URL path, and a
// --version of `../../other` would otherwise ask for a file somewhere else.
var version = regexp.MustCompile(`^v?[0-9]+\.[0-9]+(\.[0-9]+)?(-[0-9A-Za-z.]+)?$`)

// requestedVersion reads what --version was given. Empty stays empty: that is
// the request for the latest.
//
// No comparison with the running version beyond equality, on purpose. `mnemo
// update --version v0.5.0` on a 0.6.0 machine is somebody going back because
// 0.6.0 broke something for them, and refusing it would take away the one way
// out they have.
func requestedVersion(wanted string) (string, error) {
	wanted = strings.TrimSpace(wanted)
	if wanted == "" {
		return "", nil
	}
	if !version.MatchString(wanted) {
		return "", refuse("%q is not a version. Versions look like v0.6.0. Leave --version out to get the "+
			"latest release.", wanted)
	}
	return plainVersion(wanted), nil
}

// plainVersion is a version as `mnemo version` prints it and as the archive
// names carry it: no leading v, no surrounding space.
func plainVersion(value string) string {
	return strings.TrimPrefix(strings.TrimSpace(value), "v")
}

// packageManagers are the directories that mean somebody else owns this file,
// each with the command that updates it.
//
// Matched on whole path elements. A substring match would refuse an update for
// anybody whose home directory happens to be called `scoop`, and a person told
// to run a package manager they do not have has no way forward at all.
var packageManagers = []struct {
	elements []string
	name     string
	command  string
}{
	{[]string{"cellar"}, "Homebrew", "brew upgrade mnemo"},
	{[]string{"scoop", "apps"}, "Scoop", "scoop update mnemo"},
}

func refusePackageManager(executable string) error {
	elements := strings.Split(strings.ToLower(filepath.ToSlash(executable)), "/")
	for _, manager := range packageManagers {
		if !containsRun(elements, manager.elements) {
			continue
		}
		return refuse("This mnemo was installed by %s (%s), so %s owns the file and an update written over it "+
			"would be undone by the next %s command. Update it with: %s\nNothing was changed.",
			manager.name, executable, manager.name, strings.ToLower(manager.name), manager.command)
	}
	return nil
}

// containsRun reports whether the elements appear in order, next to each other.
func containsRun(elements, run []string) bool {
	for start := 0; start+len(run) <= len(elements); start++ {
		found := true
		for offset, want := range run {
			if elements[start+offset] != want {
				found = false
				break
			}
		}
		if found {
			return true
		}
	}
	return false
}
