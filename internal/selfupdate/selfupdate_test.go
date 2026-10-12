package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// Every test here runs against a release served from this process. Nothing
// reaches the network, and the archive, the checksums and the index are built in
// the test so that a test can serve a release that is wrong in exactly one way.

const (
	oldBinary = "#!/bin/sh\necho mnemo 0.5.0\n"
	newBinary = "#!/bin/sh\necho mnemo 0.6.0\n"
)

// release is a fake GitHub release: one archive, its checksums, and the index
// that names the latest version.
type release struct {
	latest string
	// archives is what the download directory holds, by file name.
	archives map[string][]byte
	// checksums is what checksums.txt says, by file name. Built from archives
	// unless a test has written something else into it.
	checksums map[string]string

	mu       sync.Mutex
	requests []string
}

func newRelease(latest string) *release {
	return &release{latest: latest, archives: map[string][]byte{}, checksums: map[string]string{}}
}

// hold puts an archive in the release and records its true checksum.
func (r *release) hold(name string, archive []byte) {
	sum := sha256.Sum256(archive)
	r.archives[name] = archive
	r.checksums[name] = hex.EncodeToString(sum[:])
}

func (r *release) serve(t *testing.T) Options {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		r.requests = append(r.requests, req.URL.Path)
		r.mu.Unlock()

		switch {
		case req.URL.Path == "/releases/latest":
			fmt.Fprintf(w, `{"tag_name":%q}`, r.latest)
		case strings.HasSuffix(req.URL.Path, "/checksums.txt"):
			var lines []string
			for name, sum := range r.checksums {
				lines = append(lines, sum+"  "+name)
			}
			fmt.Fprint(w, strings.Join(lines, "\n")+"\n")
		default:
			archive, ok := r.archives[filepath.Base(req.URL.Path)]
			if !ok {
				http.NotFound(w, req)
				return
			}
			w.Write(archive)
		}
	}))
	t.Cleanup(server.Close)
	return Options{LatestRelease: server.URL + "/releases/latest", Downloads: server.URL + "/download"}
}

func (r *release) asked() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.requests...)
}

// tarball is what a Unix release archive looks like: the binary, the licence and
// the README, with the binary named plainly.
func tarball(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var raw bytes.Buffer
	compressed := gzip.NewWriter(&raw)
	entries := tar.NewWriter(compressed)
	for name, content := range files {
		header := &tar.Header{Name: name, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg}
		if err := entries.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := entries.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := entries.Close(); err != nil {
		t.Fatal(err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}
	return raw.Bytes()
}

func zipball(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var raw bytes.Buffer
	entries := zip.NewWriter(&raw)
	for name, content := range files {
		file, err := entries.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := entries.Close(); err != nil {
		t.Fatal(err)
	}
	return raw.Bytes()
}

// installed puts a file where mnemo is, and answers with its path.
func installed(t *testing.T, directory, name, content string) string {
	t.Helper()
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func slurp(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

// unixRelease is the common case: one 0.6.0 release for this machine's build.
func unixRelease(t *testing.T) (*release, Options, string) {
	t.Helper()
	rel := newRelease("v0.6.0")
	name := archiveName("0.6.0", "linux", "amd64")
	rel.hold(name, tarball(t, map[string]string{
		"mnemo":     newBinary,
		"README.md": "# mnemo\n",
		"LICENSE":   "MIT\n",
	}))
	opts := rel.serve(t)
	opts.Running = "0.5.0"
	opts.OS, opts.Arch = "linux", "amd64"
	opts.Executable = installed(t, filepath.Join(t.TempDir(), "bin"), "mnemo", oldBinary)
	return rel, opts, name
}

func TestTheRunningVersionBeingTheLatestChangesNothing(t *testing.T) {
	rel, opts, _ := unixRelease(t)
	opts.Running = "v0.6.0" // the running version may carry its v; the tag does.

	result, err := Update(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if !result.UpToDate || result.To != "0.6.0" {
		t.Errorf("result = %+v", result)
	}
	if result.Line() != "mnemo 0.6.0 is up to date" {
		t.Errorf("line = %q", result.Line())
	}
	if slurp(t, opts.Executable) != oldBinary {
		t.Error("the executable was replaced although it was already current")
	}
	// Only the index was asked for: finding out there is nothing to do must not
	// cost a download.
	if asked := rel.asked(); len(asked) != 1 || asked[0] != "/releases/latest" {
		t.Errorf("asked for %v", asked)
	}
}

func TestAChecksumMismatchChangesNothing(t *testing.T) {
	rel, opts, name := unixRelease(t)
	rel.checksums[name] = strings.Repeat("0", 64)
	before := slurp(t, opts.Executable)

	_, err := Update(context.Background(), opts)
	if err == nil {
		t.Fatal("a download that does not match its checksum was accepted")
	}
	if !IsRefusal(err) {
		t.Errorf("a bad download is something to show a person, not a fault: %v", err)
	}
	if !strings.Contains(err.Error(), "nothing was changed") {
		t.Errorf("the refusal does not say the machine is untouched: %v", err)
	}
	if after := slurp(t, opts.Executable); after != before {
		t.Error("the executable is not byte-identical after a checksum mismatch")
	}
	// Not even a temporary file: the directory holds what it held.
	entries, err := os.ReadDir(filepath.Dir(opts.Executable))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("the directory holds %d entries, so something was written: %v", len(entries), entries)
	}
}

func TestAnArchiveWithoutTheBinaryIsRefused(t *testing.T) {
	rel, opts, name := unixRelease(t)
	rel.hold(name, tarball(t, map[string]string{"README.md": "# mnemo\n"}))

	_, err := Update(context.Background(), opts)
	if err == nil || !strings.Contains(err.Error(), "no mnemo") {
		t.Fatalf("err = %v", err)
	}
	if slurp(t, opts.Executable) != oldBinary {
		t.Error("the executable was touched although the archive held no binary")
	}
}

func TestAnArchiveThatIsNotAnArchiveIsRefused(t *testing.T) {
	rel, opts, name := unixRelease(t)
	rel.hold(name, []byte("<html>404 from a proxy</html>"))

	_, err := Update(context.Background(), opts)
	if err == nil || !strings.Contains(err.Error(), "not a gzip file") {
		t.Fatalf("err = %v", err)
	}
	if slurp(t, opts.Executable) != oldBinary {
		t.Error("the executable was touched although the archive was not one")
	}
}

func TestAVersionThatDoesNotExistSaysSo(t *testing.T) {
	_, opts, _ := unixRelease(t)
	opts.Wanted = "v9.9.9"

	_, err := Update(context.Background(), opts)
	if err == nil || !IsRefusal(err) || !strings.Contains(err.Error(), "404") {
		t.Fatalf("err = %v", err)
	}
}

func TestSomethingThatIsNotAVersionIsRefusedBeforeAnythingIsAsked(t *testing.T) {
	rel, opts, _ := unixRelease(t)
	opts.Wanted = "../../../etc/passwd"

	_, err := Update(context.Background(), opts)
	if err == nil || !IsRefusal(err) {
		t.Fatalf("err = %v", err)
	}
	if asked := rel.asked(); len(asked) != 0 {
		t.Errorf("a version that cannot be one still asked for %v", asked)
	}
}

// A package manager owns the file it installed. Writing over it would be undone
// by the manager's next command, and the person would never know which mnemo
// they are running.
func TestAnExecutableUnderAPackageManagerIsSentBackToIt(t *testing.T) {
	cases := []struct {
		name      string
		directory string
		words     string
	}{
		{"a Homebrew Cellar", filepath.Join("opt", "homebrew", "Cellar", "mnemo", "0.5.0", "bin"), "brew upgrade mnemo"},
		{"a Scoop app directory", filepath.Join("scoop", "apps", "mnemo", "current"), "scoop update mnemo"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rel, opts, _ := unixRelease(t)
			opts.Executable = installed(t, filepath.Join(t.TempDir(), c.directory), "mnemo", oldBinary)

			_, err := Update(context.Background(), opts)
			if err == nil || !IsRefusal(err) {
				t.Fatalf("err = %v", err)
			}
			if !strings.Contains(err.Error(), c.words) {
				t.Errorf("the refusal does not tell the person to run %q:\n%v", c.words, err)
			}
			// Refused before the download, so a machine mnemo will not update is
			// also a machine it does not pull a release onto.
			for _, path := range rel.asked() {
				if strings.Contains(path, "/download/") {
					t.Errorf("it downloaded %s before refusing", path)
				}
			}
		})
	}
}

// A directory that merely contains the word is not a package manager: refusing
// there would tell somebody to run a manager they do not have, which leaves
// them with no way forward at all.
func TestADirectoryThatOnlyLooksLikeAPackageManagerIsNotRefused(t *testing.T) {
	_, opts, _ := unixRelease(t)
	opts.Executable = installed(t, filepath.Join(t.TempDir(), "cellarmaster", "bin"), "mnemo", oldBinary)

	if _, err := Update(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if slurp(t, opts.Executable) != newBinary {
		t.Error("the update did not take")
	}
}

// The executable is replaced by a rename and never written in place: anything
// already executing the old file must keep reading a whole binary. Proved with a
// hard link, because nothing else tells a rename from a write in place
// afterwards.
func TestTheReplacementIsARenameAndNotAWriteInPlace(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the Unix path is what this proves")
	}
	_, opts, _ := unixRelease(t)
	witness := opts.Executable + ".witness"
	if err := os.Link(opts.Executable, witness); err != nil {
		t.Skipf("this filesystem has no hard links: %v", err)
	}

	if _, err := Update(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if slurp(t, opts.Executable) != newBinary {
		t.Fatal("the update did not take")
	}
	if slurp(t, witness) != oldBinary {
		t.Error("the executable was written in place, so a running mnemo could read half a binary")
	}
}

// A symlink on the PATH is followed, so the file that gets replaced is the real
// one. Replacing the link instead would leave the old binary on disk and break
// an install that is deliberately a link.
func TestASymlinkOnThePathIsFollowedToTheRealFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need a privilege on Windows")
	}
	_, opts, _ := unixRelease(t)
	real := opts.Executable
	link := filepath.Join(t.TempDir(), "mnemo")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("this machine will not make a symlink: %v", err)
	}
	opts.Executable = link

	result, err := Update(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if result.Executable != real {
		t.Errorf("it replaced %s rather than the file the link points at", result.Executable)
	}
	if slurp(t, real) != newBinary {
		t.Error("the real file still holds the old binary")
	}
	if target, err := os.Readlink(link); err != nil || target != real {
		t.Errorf("the link no longer points at the real file: %q, %v", target, err)
	}
}

// The mode of the file that was there is kept, because an install can be
// deliberately narrower than 0755.
func TestTheModeOfTheOldExecutableIsKept(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes do not mean this on Windows")
	}
	_, opts, _ := unixRelease(t)
	if err := os.Chmod(opts.Executable, 0o700); err != nil {
		t.Fatal(err)
	}

	if _, err := Update(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(opts.Executable)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Errorf("mode = %v, want 0700", info.Mode().Perm())
	}
}

// Windows cannot replace the running image, so the old one is renamed aside and
// the next mnemo command deletes it. Driven here with Options.OS, because
// otherwise this path is first walked by a user.
func TestOnWindowsTheRunningExecutableIsMovedAside(t *testing.T) {
	rel := newRelease("v0.6.0")
	name := archiveName("0.6.0", "windows", "amd64")
	rel.hold(name, zipball(t, map[string]string{"mnemo.exe": newBinary, "README.md": "# mnemo\n"}))
	opts := rel.serve(t)
	opts.Running, opts.OS, opts.Arch = "0.5.0", "windows", "amd64"
	opts.Executable = installed(t, filepath.Join(t.TempDir(), "bin"), "mnemo.exe", oldBinary)

	if _, err := Update(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if slurp(t, opts.Executable) != newBinary {
		t.Error("the update did not take")
	}
	aside := opts.Executable + ".old"
	if slurp(t, aside) != oldBinary {
		t.Error("the old executable was not moved aside, so a running mnemo lost the file under it")
	}

	RemoveOldExecutable(opts.Executable)
	if _, err := os.Stat(aside); !os.IsNotExist(err) {
		t.Errorf("the leftover is still there: %v", err)
	}
}

// An update that is already waiting from a previous one does not stop this one.
func TestAnOldExecutableLeftFromBeforeIsReplaced(t *testing.T) {
	rel := newRelease("v0.6.0")
	name := archiveName("0.6.0", "windows", "amd64")
	rel.hold(name, zipball(t, map[string]string{"mnemo.exe": newBinary}))
	opts := rel.serve(t)
	opts.Running, opts.OS, opts.Arch = "0.5.0", "windows", "amd64"
	directory := filepath.Join(t.TempDir(), "bin")
	opts.Executable = installed(t, directory, "mnemo.exe", oldBinary)
	installed(t, directory, "mnemo.exe.old", "a much older mnemo")

	if _, err := Update(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if slurp(t, opts.Executable+".old") != oldBinary {
		t.Error("the leftover from the previous update was kept instead of the one just replaced")
	}
}

// Steps 5 and 6 happen after the replacement and only after it. Nothing is
// restarted for an update that did not happen: a restart that follows a refusal
// stops a working server for no reason at all.
func TestWhatRunsInTheBackgroundIsRestartedOnlyAfterAReplacement(t *testing.T) {
	type calls struct{ service, tunnel, plugin int }

	wire := func(opts *Options, counted *calls) {
		opts.RestartService = func(context.Context) error { counted.service++; return nil }
		opts.RestartTunnel = func(context.Context) error { counted.tunnel++; return nil }
		opts.UpdatePlugin = func(context.Context) error { counted.plugin++; return nil }
	}

	t.Run("after an update, each one once", func(t *testing.T) {
		_, opts, _ := unixRelease(t)
		var counted calls
		wire(&opts, &counted)

		result, err := Update(context.Background(), opts)
		if err != nil {
			t.Fatal(err)
		}
		if counted != (calls{1, 1, 1}) {
			t.Errorf("calls = %+v", counted)
		}
		if !result.ServiceRestarted || !result.TunnelRestarted || !result.PluginUpdated {
			t.Errorf("result = %+v", result)
		}
		if result.Line() != "Updated to 0.6.0. Open chats use the new version after they restart." {
			t.Errorf("line = %q", result.Line())
		}
	})

	t.Run("not when it is already up to date", func(t *testing.T) {
		_, opts, _ := unixRelease(t)
		opts.Running = "0.6.0"
		var counted calls
		wire(&opts, &counted)

		if _, err := Update(context.Background(), opts); err != nil {
			t.Fatal(err)
		}
		if counted != (calls{}) {
			t.Errorf("calls = %+v", counted)
		}
	})

	t.Run("not when the checksum does not match", func(t *testing.T) {
		rel, opts, name := unixRelease(t)
		rel.checksums[name] = strings.Repeat("0", 64)
		var counted calls
		wire(&opts, &counted)

		if _, err := Update(context.Background(), opts); err == nil {
			t.Fatal("the mismatch was accepted")
		}
		if counted != (calls{}) {
			t.Errorf("calls = %+v", counted)
		}
	})
}

// Nothing a caller did not give is called, so a machine with no service and no
// plugin updates without either being invented for it.
func TestWithoutTheFunctionsNothingIsRestarted(t *testing.T) {
	_, opts, _ := unixRelease(t)

	result, err := Update(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if result.ServiceRestarted || result.TunnelRestarted || result.PluginUpdated {
		t.Errorf("result = %+v", result)
	}
	if len(result.Trouble) != 0 {
		t.Errorf("trouble = %v", result.Trouble)
	}
}

// The binary is already in place by then, so a failed restart is reported next
// to a success and never instead of it: a caller that saw an error here would
// tell the person the update did not happen.
func TestAFailedRestartIsReportedButTheUpdateStillHappened(t *testing.T) {
	_, opts, _ := unixRelease(t)
	opts.RestartService = func(context.Context) error { return fmt.Errorf("the unit would not start") }
	opts.UpdatePlugin = func(context.Context) error { return fmt.Errorf("claude is not on the PATH") }

	result, err := Update(context.Background(), opts)
	if err != nil {
		t.Fatalf("a failed restart was reported as a failed update: %v", err)
	}
	if slurp(t, opts.Executable) != newBinary {
		t.Error("the update did not take")
	}
	if result.ServiceRestarted || result.PluginUpdated {
		t.Errorf("result = %+v", result)
	}
	if len(result.Trouble) != 2 {
		t.Fatalf("trouble = %v", result.Trouble)
	}
	for _, want := range []string{"mnemo server restart", "claude plugin update mnemo@mnemo"} {
		if !strings.Contains(strings.Join(result.Trouble, "\n"), want) {
			t.Errorf("the trouble does not say to run %q:\n%v", want, result.Trouble)
		}
	}
}

// --version is the way back when a release breaks something, so an older
// version is installed and not argued with.
func TestAnOlderVersionAskedForByNameIsInstalled(t *testing.T) {
	rel := newRelease("v0.6.0")
	name := archiveName("0.4.0", "linux", "amd64")
	rel.hold(name, tarball(t, map[string]string{"mnemo": "#!/bin/sh\necho mnemo 0.4.0\n"}))
	opts := rel.serve(t)
	opts.Running, opts.OS, opts.Arch, opts.Wanted = "0.6.0", "linux", "amd64", "v0.4.0"
	opts.Executable = installed(t, filepath.Join(t.TempDir(), "bin"), "mnemo", oldBinary)

	result, err := Update(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if result.To != "0.4.0" || result.UpToDate {
		t.Errorf("result = %+v", result)
	}
	// The index is not even asked for: the version was given.
	for _, path := range rel.asked() {
		if path == "/releases/latest" {
			t.Error("it asked which version is the latest although one was named")
		}
	}
}

// A build from a working copy has no version to compare, so it updates rather
// than claiming to be current.
func TestABuildWithNoVersionUpdates(t *testing.T) {
	_, opts, _ := unixRelease(t)
	opts.Running = "dev"

	result, err := Update(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if result.UpToDate || result.To != "0.6.0" {
		t.Errorf("result = %+v", result)
	}
}

func TestTheArchiveNamesFollowTheBuildSection(t *testing.T) {
	cases := map[string]string{
		archiveName("0.6.0", "linux", "amd64"):   "mnemo_0.6.0_linux_amd64.tar.gz",
		archiveName("0.6.0", "darwin", "arm64"):  "mnemo_0.6.0_darwin_arm64.tar.gz",
		archiveName("0.6.0", "windows", "amd64"): "mnemo_0.6.0_windows_amd64.zip",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("name = %q, want %q", got, want)
		}
	}
}

func TestAChecksumIsFoundWhateverTheLineLooksLike(t *testing.T) {
	const sum = "4f1d6e4a2b3c5d6e7f8091a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e6"
	listing := "deadbeef  mnemo_0.6.0_linux_arm64.tar.gz\n" +
		sum + " *mnemo_0.6.0_linux_amd64.tar.gz\n"

	got, ok := checksumFor(listing, "mnemo_0.6.0_linux_amd64.tar.gz")
	if !ok || got != sum {
		t.Errorf("checksumFor = %q, %v", got, ok)
	}
	if _, ok := checksumFor(listing, "mnemo_0.6.0_windows_amd64.zip"); ok {
		t.Error("a file that is not listed was found anyway")
	}
}

// An archive is read into memory, never onto the disk, so a release that turns
// out to be wrong leaves nothing behind. The ceiling is what keeps that honest.
func TestABodyWithoutAnEndIsRefused(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		block := bytes.Repeat([]byte("x"), 1<<20)
		for sent := 0; sent <= mostBytes; sent += len(block) {
			if _, err := w.Write(block); err != nil {
				return
			}
		}
	}))
	defer server.Close()

	opts := Options{Client: server.Client(), OS: "linux", Arch: "amd64"}
	if _, err := fetch(context.Background(), opts, server.URL); err == nil {
		t.Fatal("a body with no end was read to the end")
	}
}
