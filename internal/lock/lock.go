// Package lock serialises writes to a directory: between processes, and between
// the goroutines of one process.
//
// Several agents write to one store at the same time, and the HTTP server runs
// many requests in one process, so both layers are needed. The operating system
// releases a file lock when its process dies, which is why there is no stale-lock
// detection here: a crash frees the lock by itself.
//
// The lock file lives outside the directory it guards, so it never shows up in
// git status and a read-only checkout can still be locked.
package lock

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/flock"
)

const (
	// DefaultTimeout is how long Acquire waits when the context has no deadline.
	DefaultTimeout = 10 * time.Second
	// retryDelay is how often the file lock is retried while waiting.
	retryDelay = 50 * time.Millisecond
)

// TimeoutError says that someone else is holding the lock. It is an error a
// caller can show: the file names which lock, and the wait says for how long.
type TimeoutError struct {
	File string
	Wait time.Duration
}

func (e *TimeoutError) Error() string {
	return fmt.Sprintf("could not take the mnemo lock at %s after %s: another writer is holding it", e.File, e.Wait)
}

// IsTimeout reports whether an error is a lock timeout.
func IsTimeout(err error) bool {
	var timeout *TimeoutError
	return errors.As(err, &timeout)
}

var notNameSafe = regexp.MustCompile(`[^A-Za-z0-9]+`)

// Key turns the directory being guarded into the name of its lock file.
//
// It resolves symbolic links first, so two names for one store — a link, a
// relative path — share a lock instead of each taking their own.
func Key(guarded string) string {
	resolved, err := filepath.EvalSymlinks(guarded)
	if err != nil {
		// The directory may not exist yet, which is normal before a bootstrap.
		if resolved, err = filepath.Abs(guarded); err != nil {
			resolved = guarded
		}
	}
	return strings.Trim(notNameSafe.ReplaceAllString(resolved, "_"), "_")
}

// A Locker hands out the locks kept in one directory, normally <state>/locks.
type Locker struct {
	dir string

	mu    sync.Mutex
	gates map[string]chan struct{}
}

// New returns a Locker that keeps its lock files in dir.
func New(dir string) *Locker {
	return &Locker{dir: dir, gates: map[string]chan struct{}{}}
}

// Dir is where the lock files are kept.
func (l *Locker) Dir() string { return l.dir }

// File is the lock file that guards a directory.
func (l *Locker) File(guarded string) string {
	return filepath.Join(l.dir, Key(guarded)+".lock")
}

// gate is the one-slot semaphore that serialises this process's own goroutines
// for a lock file. A mutex would not do: a caller that is already holding the
// lock must fail with a timeout instead of waiting for itself forever.
func (l *Locker) gate(file string) chan struct{} {
	l.mu.Lock()
	defer l.mu.Unlock()
	if g, ok := l.gates[file]; ok {
		return g
	}
	g := make(chan struct{}, 1)
	l.gates[file] = g
	return g
}

// Acquire takes the lock for a directory and returns the function that releases
// it. Releasing twice is harmless.
//
// It waits until the context is done, or for DefaultTimeout when the context has
// no deadline of its own. A caller that already holds this lock times out rather
// than deadlocking: the lock is not reentrant, and saying so loudly is the point.
func (l *Locker) Acquire(ctx context.Context, guarded string) (func(), error) {
	if err := os.MkdirAll(l.dir, 0o700); err != nil {
		return nil, fmt.Errorf("creating the lock directory: %w", err)
	}
	file := l.File(guarded)

	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultTimeout)
		defer cancel()
	}
	started := time.Now()

	select {
	case l.gate(file) <- struct{}{}:
	case <-ctx.Done():
		return nil, &TimeoutError{File: file, Wait: time.Since(started)}
	}
	releaseGate := func() { <-l.gate(file) }

	handle := flock.New(file)
	taken, err := handle.TryLockContext(ctx, retryDelay)
	switch {
	case taken:
	case err == nil || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled):
		// Running out of time is what contention looks like from here: another
		// writer is holding the file. It reaches the caller as a timeout, not as a
		// context error, because a context is not something they set.
		releaseGate()
		return nil, &TimeoutError{File: file, Wait: time.Since(started)}
	default:
		releaseGate()
		return nil, fmt.Errorf("taking the lock at %s: %w", file, err)
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			// Unlocking can only fail if the handle is already gone, which is the
			// state the caller wanted anyway.
			_ = handle.Unlock()
			releaseGate()
		})
	}, nil
}

// Hold runs fn with the lock held, and releases it however fn ends.
func (l *Locker) Hold(ctx context.Context, guarded string, fn func() error) error {
	release, err := l.Acquire(ctx, guarded)
	if err != nil {
		return err
	}
	defer release()
	return fn()
}
