// Package store owns the user's memory on disk: creating it, writing to it, and
// committing what changed.
//
// Reading is in internal/memory, which needs no lock and no git. Everything here
// changes something, so everything here takes the store's lock first and writes
// atomically. The rules it enforces are the mechanical half of the data
// contract — an id that is a file name, a project that exists before a memory
// can be tagged with it, a secret that never reaches the disk. The other half,
// what deserves to be written at all, needs the conversation, which this process
// cannot see.
package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/AlexParco/mnemo/internal/gitx"
	"github.com/AlexParco/mnemo/internal/lock"
	"github.com/AlexParco/mnemo/internal/memory"
)

// A Refusal is an answer, not a failure: something the caller can act on, in
// words they can pass to a person. Anything else that goes wrong is a plain
// error and reads like a bug, because it is one.
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
	return asRefusal(err, &refusal)
}

// MemoryTypes are the six kinds of fact a memory can hold, in the order the
// contract gives them.
var MemoryTypes = memory.TypeOrder

var kebab = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// coAuthored matches the trailer a commit message must not carry into the store.
var coAuthored = regexp.MustCompile(`(?i)^\s*Co-Authored-By\s*:`)

// IsKebab reports whether a value may be used as a slug or a memory id. Every
// value that reaches a path is checked with it first: a slug with a slash or a
// pair of dots would otherwise walk out of the store.
func IsKebab(s string) bool { return kebab.MatchString(s) }

// Options configure a Store. The zero value of each field is the sensible one.
type Options struct {
	// Locker serialises writes. Without one, a Store makes its own under the
	// store's parent directory, which is only right for a throwaway store.
	Locker *lock.Locker
	// Remote is the hub this store syncs with, wired on the first bootstrap.
	Remote string
	// Now is where dates come from. Tests pin it.
	Now func() time.Time
}

// A Store is one memory directory and the git repository inside it.
type Store struct {
	Dir string

	repo   *gitx.Repo
	locker *lock.Locker
	remote string
	now    func() time.Time
}

// New returns a Store for a directory, which need not exist yet.
func New(dir string, opts Options) *Store {
	s := &Store{
		Dir:    dir,
		repo:   gitx.New(dir),
		locker: opts.Locker,
		remote: opts.Remote,
		now:    opts.Now,
	}
	if s.locker == nil {
		s.locker = lock.New(filepath.Join(filepath.Dir(dir), ".mnemo-locks"))
	}
	if s.now == nil {
		s.now = time.Now
	}
	return s
}

// Repo exposes the git repository, for the sync and push operations that live in
// internal/gitx and are driven from above.
func (s *Store) Repo() *gitx.Repo { return s.repo }

// Today is the local calendar date, which is the granularity `updated` uses.
func (s *Store) Today() string { return s.now().Format("2006-01-02") }

// hold runs fn with the store's lock held.
//
// Running out of time waiting for the lock is a refusal, not a fault: another
// agent is saving right now and trying again in a moment is exactly the right
// thing to do. Reported as an unexpected failure it would read as mnemo being
// broken, and the caller would stop instead of retrying.
func (s *Store) hold(ctx context.Context, fn func() error) error {
	err := s.locker.Hold(ctx, s.Dir, fn)
	if lock.IsTimeout(err) {
		return refuse("Another writer is holding the store and did not let go in time (%s). "+
			"Something else is saving right now: wait a moment and try again.", err)
	}
	return err
}

// requireSettled refuses to change the store while a merge is half-applied.
//
// Git lets you commit mid-rebase, and says nothing. The commit is folded into
// the rebase, and `git rebase --abort` then destroys it without a word and
// without stashing it — so a session's whole memory can vanish because an agent
// did the two things it was told to do, in the wrong order. On top of that the
// files on disk carry conflict markers, so anything written on top of them is
// written onto half a merge.
//
// Resolving, rebasing and syncing are exempt: they are the way out.
func (s *Store) requireSettled() error {
	if !s.repo.Status().Rebasing {
		return nil
	}
	return refuse("This store is mid-merge: a sync left a rebase in progress, and the files on disk carry " +
		"conflict markers. Nothing was written, because a change made now would be folded into the rebase and " +
		"lost if it is aborted. Call mnemo_sync to get the conflicted files with their contents, send each one " +
		"back merged with mnemo_resolve_conflict, then call mnemo_rebase with action \"continue\".")
}

// tempPrefix marks the files an interrupted write leaves behind. It starts with
// a dot and does not end in .md, so a listing of memories never picks one up.
const tempPrefix = ".mnemo-tmp-"

// writeFile replaces a file in one step.
//
// A reader holding no lock must never see half a file, so the content goes to a
// temporary name in the same directory — the same filesystem, so the rename is
// atomic — and then over the target.
func writeFile(path, content string) error {
	dir, base := filepath.Split(path)
	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		return fmt.Errorf("naming a temporary file: %w", err)
	}
	temp := filepath.Join(dir, "."+base+tempPrefix+hex.EncodeToString(suffix))

	if err := os.WriteFile(temp, []byte(content), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := os.Rename(temp, path); err != nil {
		os.Remove(temp)
		return fmt.Errorf("replacing %s: %w", path, err)
	}
	return nil
}

// cleanTemporaries removes what an interrupted write left behind. Anything newer
// than a minute may belong to a write that is still running in another process.
func cleanTemporaries(dir string, now time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !strings.Contains(e.Name(), tempPrefix) {
			continue
		}
		info, err := e.Info()
		if err != nil || now.Sub(info.ModTime()) < time.Minute {
			continue
		}
		os.Remove(filepath.Join(dir, e.Name()))
	}
}

// refuseSecrets stops content that carries something that looks like a
// credential. The push scan is the second line; this is the first, and it runs
// before anything reaches the disk.
func refuseSecrets(what, content string) error {
	findings := gitx.ScanContent(what, content)
	if len(findings) == 0 {
		return nil
	}
	lines := []string{fmt.Sprintf(
		"That looks like a secret (%s). Nothing was written. Keep the value where secrets belong and save a note that says where it lives, not what it is.",
		findings[0].Rule)}
	for _, f := range findings {
		lines = append(lines, fmt.Sprintf("  line %d: %s", f.Line, f.Excerpt))
	}
	return refuse("%s", strings.Join(lines, "\n"))
}

// defaultAuthor is who a memory is attributed to when the caller says nothing.
func (s *Store) defaultAuthor() string {
	if id, ok := s.repo.Identity(); ok && id.Name != "" {
		return id.Name
	}
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return "unknown"
}

// requireProjects refuses a memory tagged with a project that does not exist.
// Creating one silently would scatter a typo across the store as a new project
// nobody meant to make.
func (s *Store) requireProjects(slugs []string) error {
	if len(slugs) == 0 {
		return refuse("A memory needs at least one project in `projects`.")
	}
	var missing []string
	for _, slug := range slugs {
		if !IsKebab(slug) || !memory.ProjectExists(s.Dir, slug) {
			missing = append(missing, "'"+slug+"'")
		}
	}
	if len(missing) == 0 {
		return nil
	}
	existing := memory.ProjectSlugs(s.Dir)
	message := fmt.Sprintf("No project(s) %s in the store. ", strings.Join(missing, ", "))
	if len(existing) > 0 {
		message += "Existing: " + strings.Join(existing, ", ") + ". "
	}
	return refuse("%sFix the slug, or create the project with mnemo_upsert_project first — never silently.", message)
}
