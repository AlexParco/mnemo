// Package gitx runs the system git against a store.
//
// It runs the real git, not a library: mnemo needs rebase and a real merge, and
// the Go implementations of git do not have them.
//
// Three ways to run a command, because three things can be meant by one:
// asking a question, performing an action, and trying something that is expected
// to fail sometimes. A pull that hits a conflict is a normal outcome, not an
// exception, and a failed write must never look like a successful one.
package gitx

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Error is a git command that failed, carrying git's own explanation.
type Error struct {
	Args   []string
	Stderr string
}

func (e *Error) Error() string {
	stderr := strings.TrimSpace(e.Stderr)
	if stderr == "" {
		stderr = "no stderr"
	}
	return fmt.Sprintf("git %s failed: %s", strings.Join(e.Args, " "), stderr)
}

// A Repo is a directory git commands run against.
type Repo struct {
	Dir string
}

// New returns a Repo for a directory, which need not exist yet.
func New(dir string) *Repo { return &Repo{Dir: dir} }

// env is the environment every git command runs with. Git must never stop to
// ask a person something: this runs with no terminal attached, and a prompt
// would hang the agent that is waiting for the answer.
func (r *Repo) env() []string {
	return append(os.Environ(), "GIT_EDITOR=true", "GIT_TERMINAL_PROMPT=0")
}

func (r *Repo) command(args []string) *exec.Cmd {
	cmd := exec.Command("git", append([]string{"-C", r.Dir}, args...)...)
	cmd.Env = r.env()
	return cmd
}

// Try asks git a question. It returns the trimmed output, and false when git
// could not answer: an empty directory, an unborn branch, a ref that does not
// exist. Callers turn that into "unknown", never into an error.
func (r *Repo) Try(args ...string) (string, bool) {
	out, err := r.command(args).Output()
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(out)), true
}

// Run performs an action. Its failure is an error carrying git's stderr.
func (r *Repo) Run(args ...string) (string, error) {
	cmd := r.command(args)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", &Error{Args: args, Stderr: stderr.String()}
	}
	return strings.TrimSpace(string(out)), nil
}

// Attempt is the result of a command that is expected to fail sometimes.
type Attempt struct {
	OK     bool
	Stdout string
	Stderr string
}

// Detail is what to tell a person: git's own words, whichever stream it used.
func (a Attempt) Detail() string {
	if a.Stderr != "" {
		return a.Stderr
	}
	return a.Stdout
}

// Attempt runs a command whose failure is a normal outcome, such as a pull that
// hits a conflict, and hands back both streams.
func (r *Repo) Attempt(args ...string) Attempt {
	cmd := r.command(args)
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	return Attempt{
		OK:     err == nil,
		Stdout: strings.TrimSpace(stdout.String()),
		Stderr: strings.TrimSpace(stderr.String()),
	}
}

// Init creates the repository, with main as its first branch.
func (r *Repo) Init() error {
	_, err := r.Run("init", "-q", "-b", "main")
	return err
}

// IsOwnRepo reports whether the directory is a repository of its own.
//
// Asking git whether this is a repository is not enough: it answers yes for any
// path inside one. A store created under another project would look initialised
// while belonging to that project, every commit would land there, and `add -A`
// would sweep up whatever the user had left uncommitted. Comparing the toplevel
// is what tells the two apart.
func (r *Repo) IsOwnRepo() bool {
	top, ok := r.Try("rev-parse", "--show-toplevel")
	if !ok {
		return false
	}
	return sameDir(top, r.Dir)
}

func sameDir(a, b string) bool {
	ra, err := filepath.EvalSymlinks(a)
	if err != nil {
		return false
	}
	rb, err := filepath.EvalSymlinks(b)
	if err != nil {
		return false
	}
	return ra == rb
}

// Identity is git's committer identity for this repository.
type Identity struct {
	Name  string
	Email string
}

// Identity returns the configured committer, and false when either half is
// missing. Without it a commit fails, and the caller can say how to fix it.
func (r *Repo) Identity() (Identity, bool) {
	name, okName := r.Try("config", "user.name")
	email, okEmail := r.Try("config", "user.email")
	if !okName || !okEmail || name == "" || email == "" {
		return Identity{}, false
	}
	return Identity{Name: name, Email: email}, true
}

// Status is where a store stands.
type Status struct {
	// IsRepo is true only for a repository of its own.
	IsRepo bool
	// Remote is the URL of origin, empty when there is none.
	Remote string
	// Branch is the current branch, empty when git cannot say.
	Branch string
	// Dirty means there are uncommitted changes in the worktree.
	Dirty bool
	// Unpushed counts the commits that are not on the hub yet.
	Unpushed int
	// UnpushedKnown is false when nothing tracks this branch, so Unpushed says
	// nothing. A count of zero and an unknown count are different answers.
	UnpushedKnown bool
	// Rebasing means the store is mid-rebase and needs resolving.
	Rebasing bool
}

// HasRemote reports whether the store is wired to a hub.
func (s Status) HasRemote() bool { return s.Remote != "" }

// Status reads where the store stands. It never fails: a directory that is not a
// repository is a valid answer.
func (r *Repo) Status() Status {
	if !r.IsOwnRepo() {
		return Status{}
	}
	status := Status{IsRepo: true}

	if remote, ok := r.Try("remote", "get-url", "origin"); ok {
		status.Remote = remote
	}
	// On a store with no commits the branch is unborn and rev-parse fails, which
	// is exactly the state right after a bootstrap. symbolic-ref still answers.
	if branch, ok := r.Try("symbolic-ref", "--short", "HEAD"); ok {
		status.Branch = branch
	} else if branch, ok := r.Try("rev-parse", "--abbrev-ref", "HEAD"); ok {
		status.Branch = branch
	}
	if porcelain, ok := r.Try("status", "--porcelain"); ok {
		status.Dirty = porcelain != ""
	}

	// Prefer the branch's own upstream; fall back to origin/main for a store
	// wired by hand, which the README documents as a supported path.
	count, ok := r.Try("rev-list", "--count", "@{u}..HEAD")
	if !ok {
		count, ok = r.Try("rev-list", "--count", "origin/main..HEAD")
	}
	if ok {
		if n, err := strconv.Atoi(count); err == nil {
			status.Unpushed, status.UnpushedKnown = n, true
		}
	}

	status.Rebasing = r.gitPathExists("rebase-merge") || r.gitPathExists("rebase-apply")
	return status
}

// gitPathExists asks git where one of its own files would be, and looks.
func (r *Repo) gitPathExists(name string) bool {
	path, ok := r.Try("rev-parse", "--git-path", name)
	if !ok {
		return false
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(r.Dir, path)
	}
	_, err := os.Stat(path)
	return err == nil
}

// Upstream is the branch this one tracks, and false when it tracks nothing.
func (r *Repo) Upstream() (string, bool) {
	return r.Try("rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")
}

// HasCommits reports whether HEAD points at anything yet.
func (r *Repo) HasCommits() bool {
	_, ok := r.Try("rev-parse", "-q", "--verify", "HEAD")
	return ok
}

// StageAll stages everything in the worktree.
func (r *Repo) StageAll() error {
	_, err := r.Run("add", "-A")
	return err
}

// Stage stages one path, given relative to the store.
func (r *Repo) Stage(path string) error {
	_, err := r.Run("add", "--", path)
	return err
}

// StagedFiles lists what a commit would carry.
func (r *Repo) StagedFiles() []string {
	out, ok := r.Try("diff", "--cached", "--name-only")
	if !ok || out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

// ConflictedFiles lists the files a merge left for someone to resolve.
func (r *Repo) ConflictedFiles() []string {
	out, ok := r.Try("diff", "--name-only", "--diff-filter=U")
	if !ok || out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

// Commit records what is staged and returns the short commit id.
func (r *Repo) Commit(message string) (string, error) {
	if _, err := r.Run("commit", "-q", "-m", message); err != nil {
		return "", err
	}
	sha, _ := r.Try("rev-parse", "--short", "HEAD")
	return sha, nil
}
