// Package mailbox is how the agents working for one person leave each other
// messages.
//
// It is deliberately not part of the memory store. Messages are ephemeral and
// memory is not: a message is read once and then it is history, while a memory is
// meant to outlive every chat that wrote it. They live in different directories,
// they sync differently — the store through the user's own git hub, the mailbox
// not at all — and conflating them would mean either syncing chatter between
// machines or losing memory to a prune.
//
// An address is `name@machine`. A name is given by `mnemo run --name`, so an
// agent that was not opened that way has memory and no mailbox. That is the whole
// admission control: there is no registration call, and nothing here grants
// authority. What a message means is decided by the agent that reads it and the
// person it is working for.
package mailbox

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/AlexParco/mnemo/internal/lock"
)

// Refusal is something the caller can act on: a name already held, a body that
// is empty, an address nobody has ever used. It is separate from a fault so that
// the surfaces above can tell an agent what to do instead.
type Refusal struct {
	Message string
}

func (r *Refusal) Error() string { return r.Message }

func refuse(format string, args ...any) error {
	return &Refusal{Message: fmt.Sprintf(format, args...)}
}

// IsRefusal reports whether an error is advice rather than a fault.
func IsRefusal(err error) bool {
	var refusal *Refusal
	return errors.As(err, &refusal)
}

// Tools are the tools an agent can run in. A group address names one of them.
var Tools = []string{"claude", "codex", "opencode", "terminal"}

// name is what `mnemo run --name` accepts.
var name = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// IsName reports whether a string can be an agent's name.
func IsName(s string) bool { return name.MatchString(s) }

// IsAddress reports whether a string is `name@machine`, both parts usable. A
// machine label is normalised to the same shape as a name before it gets here.
func IsAddress(s string) bool {
	at := strings.IndexByte(s, '@')
	return at > 0 && IsName(s[:at]) && name.MatchString(s[at+1:])
}

// Caller is who is asking. Every operation has one, and every operation records
// that it was seen.
type Caller struct {
	Name    string
	Machine string
	// Tool is one of Tools, or a label derived from an MCP client name. A derived
	// one is for display only and never matches a group address.
	Tool string
	// Instance is one process. Two chats opened with the same name are two
	// instances, and only one of them can hold the name.
	Instance string
	// Via is "local" when the process runs on the machine hosting the mailbox,
	// and "remote" when it reaches it through a server. It decides how liveness
	// is judged: a process id, or a lease.
	Via string
	// PID is meaningful only for a local caller.
	PID int
}

// Address is `name@machine`, or empty when the caller has no name.
func (c Caller) Address() string {
	if c.Name == "" || c.Machine == "" {
		return ""
	}
	return c.Name + "@" + c.Machine
}

// Named refuses a caller that has no usable name, which is how an agent that was
// not opened with `mnemo run` is kept out of the mailbox.
//
// The shape is checked here and not only in the command, because a name reaches
// the mailbox from the environment: a chat started by hand with MNEMO_AGENT set
// never goes through `mnemo run`. The name is written into `from` on every
// message and into the line a watcher turns into a notification, so a newline in
// it is a line of mnemo's own that somebody else wrote.
func (c Caller) Named() error {
	if c.Address() == "" {
		return refuse("This chat has no mailbox name. Open it with: mnemo run <tool> --name <name>")
	}
	if !IsAddress(c.Address()) {
		return refuse("'%s' is not a usable mailbox address. A name is lower-case letters, digits and "+
			"dashes, and so is a machine label. Open the chat with: mnemo run <tool> --name <name>",
			c.Address())
	}
	return nil
}

// Instance is a new instance identifier: one per process.
func Instance() string {
	value := make([]byte, 8)
	rand.Read(value)
	return hex.EncodeToString(value)
}

// Options configure a mailbox.
type Options struct {
	Locker *lock.Locker
	Now    func() time.Time
	// ProjectExists says whether a project slug exists in the store next to this
	// mailbox. The mailbox does not read the store itself: it has no business
	// knowing the store's shape, and the caller already does.
	ProjectExists func(slug string) bool
	// Alive says whether a process id is still running on this machine. Injected
	// so a test can hold a name without holding a process.
	Alive func(pid int) bool
}

// Mailbox is one machine's mailbox.
type Mailbox struct {
	Dir string
	// Machine is the label of the machine hosting it, which is what `peers`
	// reports and what a local caller's address is built from.
	Machine string

	locker        *lock.Locker
	now           func() time.Time
	projectExists func(string) bool
	alive         func(int) bool
	// moved names the directory an earlier install's files went to, so peers can
	// say it once rather than leaving somebody wondering. Guarded, because one
	// mailbox is shared by every operation in a process.
	movedMu sync.Mutex
	moved   string
}

// rememberMoved records where an earlier install's files went.
func (m *Mailbox) rememberMoved(dir string) {
	if dir == "" {
		return
	}
	m.movedMu.Lock()
	defer m.movedMu.Unlock()
	m.moved = dir
}

// Moved is the directory an earlier install's files were put in, if this process
// did the moving — once, and then never again.
//
// Said once is the promise: a person whose messages vanished on an upgrade is
// owed the directory they went to, and being told at every call for the life of
// the process is a different thing, which they would learn to skip.
func (m *Mailbox) Moved() string {
	m.movedMu.Lock()
	defer m.movedMu.Unlock()
	said := m.moved
	m.moved = ""
	return said
}

// New returns a mailbox for a directory, which need not exist yet.
func New(dir, machine string, opts Options) *Mailbox {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.ProjectExists == nil {
		// Without a store to ask, no project is known. Refusing every project
		// tag is safer than accepting one that does not exist.
		opts.ProjectExists = func(string) bool { return false }
	}
	if opts.Alive == nil {
		opts.Alive = processAlive
	}
	return &Mailbox{
		Dir: dir, Machine: machine,
		locker: opts.Locker, now: opts.Now,
		projectExists: opts.ProjectExists, alive: opts.Alive,
	}
}

// The two files. They are separate because one is append-only and one is
// replaced whole, and because a watcher reads them without the lock.
func (m *Mailbox) logPath() string   { return filepath.Join(m.Dir, "messages.jsonl") }
func (m *Mailbox) statePath() string { return filepath.Join(m.Dir, "state.json") }

// hold runs fn with the mailbox lock held. Every operation but peek takes it.
func (m *Mailbox) hold(ctx context.Context, fn func() error) error {
	if m.locker == nil {
		return fn()
	}
	err := m.locker.Hold(ctx, m.Dir, fn)
	if lock.IsTimeout(err) {
		// From inside the lock, a cancelled context and real contention look the
		// same: running out of time is what contention looks like there. Here
		// the difference matters. Nobody is holding anything — the chat went
		// away — and telling whoever is left that another agent is using the
		// mailbox would send them looking for a problem that is not there.
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return refuse("Another agent is using the mailbox and did not let go in time (%s). Try again in a moment.",
			err)
	}
	return err
}

// ensureDir creates the mailbox directory, and moves aside files written by an
// earlier install.
//
// Version 1 files have no `version` field. They are not converted: the address
// format is not the one they use, and messages are ephemeral, so starting empty
// costs a person nothing and guessing at old addresses would cost them a wrong
// reply. They are kept rather than deleted, because they are the only copy.
func (m *Mailbox) ensureDir() (moved string, err error) {
	if err := os.MkdirAll(m.Dir, 0o700); err != nil {
		return "", fmt.Errorf("creating the mailbox at %s: %w", m.Dir, err)
	}
	state, err := os.ReadFile(m.statePath())
	if err != nil {
		return "", nil
	}
	if strings.Contains(string(state), "\"version\"") {
		return "", nil
	}
	aside := filepath.Join(m.Dir, "v1-"+m.now().UTC().Format("20060102-150405"))
	if err := os.MkdirAll(aside, 0o700); err != nil {
		return "", err
	}
	for _, path := range []string{m.statePath(), m.logPath()} {
		if _, err := os.Stat(path); err != nil {
			continue
		}
		if err := os.Rename(path, filepath.Join(aside, filepath.Base(path))); err != nil {
			return "", fmt.Errorf("moving %s aside: %w", path, err)
		}
	}
	// Recorded here rather than by each caller, so no operation can do the moving
	// and forget to mention it.
	m.rememberMoved(aside)
	return aside, nil
}

// logSize is the size of the log, for a wait that watches it without the lock.
func (m *Mailbox) logSize() int64 {
	info, err := os.Stat(m.logPath())
	if err != nil {
		return -1
	}
	return info.Size()
}

// writeFile replaces a file in one step. A watcher reads without the lock, so it
// must never see half of one.
func writeFile(path string, content []byte) error {
	dir := filepath.Dir(path)
	suffix := make([]byte, 6)
	rand.Read(suffix)
	temp := filepath.Join(dir, "."+filepath.Base(path)+".mnemo-tmp-"+hex.EncodeToString(suffix))
	if err := os.WriteFile(temp, content, 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", temp, err)
	}
	if err := os.Rename(temp, path); err != nil {
		os.Remove(temp)
		return fmt.Errorf("replacing %s: %w", path, err)
	}
	return nil
}
