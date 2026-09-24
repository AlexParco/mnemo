package lock

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newLocker returns a Locker whose lock files live in their own temporary
// directory, next to a store directory that is never written to.
func newLocker(t *testing.T) (*Locker, string) {
	t.Helper()
	base := t.TempDir()
	store := filepath.Join(base, "store")
	if err := os.MkdirAll(store, 0o755); err != nil {
		t.Fatal(err)
	}
	return New(filepath.Join(base, "locks")), store
}

func TestAcquireAndRelease(t *testing.T) {
	locker, store := newLocker(t)

	release, err := locker.Acquire(t.Context(), store)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	// The lock file is where it belongs, and not inside the store.
	file := locker.File(store)
	if filepath.Dir(file) != locker.Dir() {
		t.Errorf("lock file is at %s, want it under %s", file, locker.Dir())
	}
	if strings.HasPrefix(file, store+string(filepath.Separator)) {
		t.Errorf("lock file %s is inside the directory it guards", file)
	}
	if _, err := os.Stat(file); err != nil {
		t.Errorf("the lock file was not created: %v", err)
	}

	release()
	release() // releasing twice is harmless

	release2, err := locker.Acquire(t.Context(), store)
	if err != nil {
		t.Fatalf("acquiring after a release: %v", err)
	}
	release2()
}

func TestSecondHolderTimesOut(t *testing.T) {
	locker, store := newLocker(t)

	release, err := locker.Acquire(t.Context(), store)
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err := locker.Acquire(ctx, store); !IsTimeout(err) {
		t.Fatalf("second acquire gave %v, want a timeout", err)
	} else if !strings.Contains(err.Error(), locker.File(store)) {
		t.Errorf("the message does not name the lock file: %s", err)
	}
	if waited := time.Since(started); waited < 100*time.Millisecond {
		t.Errorf("it gave up after %s, without waiting for the deadline", waited)
	}
}

// The lock is not reentrant. A caller that already holds it must fail, because a
// nested acquire would otherwise wait for itself until the process is killed.
//
// It fails by running out of time, not sooner: nothing here can tell a caller
// that is deadlocking itself apart from a second goroutine that is waiting its
// turn, which is a perfectly ordinary thing to do.
func TestNotReentrant(t *testing.T) {
	locker, store := newLocker(t)

	release, err := locker.Acquire(t.Context(), store)
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	if _, err := locker.Acquire(ctx, store); !IsTimeout(err) {
		t.Fatalf("a nested acquire gave %v, want a timeout", err)
	}
}

// When the holder is another process there is nothing local to consult, so the
// wait really happens. What matters is that it still comes back as a timeout the
// caller can act on, and not as a context error they never asked about.
func TestAnotherProcessHoldingItGivesATimeout(t *testing.T) {
	locker, store := newLocker(t)

	holder, stop := startHolder(t, locker.Dir(), store)
	defer stop()
	<-holder // wait until it has the lock

	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	_, err := locker.Acquire(ctx, store)
	if !IsTimeout(err) {
		t.Fatalf("acquiring while another process holds it gave %v (%T), want a timeout", err, err)
	}
	if !strings.Contains(err.Error(), "another writer") {
		t.Errorf("the message does not say what happened: %s", err)
	}
}

func TestWaiterGetsInAfterRelease(t *testing.T) {
	locker, store := newLocker(t)

	release, err := locker.Acquire(t.Context(), store)
	if err != nil {
		t.Fatal(err)
	}

	got := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		second, err := locker.Acquire(ctx, store)
		if err == nil {
			second()
		}
		got <- err
	}()

	time.Sleep(100 * time.Millisecond) // let the waiter start waiting
	release()

	select {
	case err := <-got:
		if err != nil {
			t.Fatalf("the waiter never got in: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the waiter was still waiting after the lock was released")
	}
}

func TestDifferentDirectoriesDoNotBlock(t *testing.T) {
	locker, store := newLocker(t)
	other := filepath.Join(filepath.Dir(store), "mailbox")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}

	release, err := locker.Acquire(t.Context(), store)
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	releaseOther, err := locker.Acquire(ctx, other)
	if err != nil {
		t.Fatalf("a second directory was blocked by the first: %v", err)
	}
	releaseOther()
}

func TestKeyIsTheSameForAliasesOfOneDirectory(t *testing.T) {
	locker, store := newLocker(t)

	link := filepath.Join(filepath.Dir(store), "link-to-store")
	if err := os.Symlink(store, link); err != nil {
		t.Skipf("symbolic links are not available here: %v", err)
	}
	if locker.File(link) != locker.File(store) {
		t.Errorf("a link takes a different lock: %s and %s", locker.File(link), locker.File(store))
	}

	// A directory that does not exist yet is normal before the first save.
	missing := filepath.Join(filepath.Dir(store), "not-created-yet")
	if key := Key(missing); key == "" || strings.ContainsAny(key, `/\.`) {
		t.Errorf("Key(%q) = %q, want a plain file name", missing, key)
	}
}

func TestHold(t *testing.T) {
	locker, store := newLocker(t)

	wanted := errors.New("what fn returned")
	if err := locker.Hold(t.Context(), store, func() error { return wanted }); !errors.Is(err, wanted) {
		t.Errorf("Hold returned %v, want the function's own error", err)
	}

	// The lock is free again even though fn failed.
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	release, err := locker.Acquire(ctx, store)
	if err != nil {
		t.Fatalf("the lock was not released after a failure: %v", err)
	}
	release()
}

// A second process must be kept out while this one holds the lock, and must get
// in once it is released. Goroutines cannot prove this: the file lock is what
// makes it true, and only another process exercises it.
func TestAnotherProcessIsKeptOut(t *testing.T) {
	locker, store := newLocker(t)

	release, err := locker.Acquire(t.Context(), store)
	if err != nil {
		t.Fatal(err)
	}

	if out := runHelper(t, locker.Dir(), store); !strings.Contains(out, "timeout") {
		t.Fatalf("the other process said %q, want it to be kept out", out)
	}

	release()

	if out := runHelper(t, locker.Dir(), store); !strings.Contains(out, "locked") {
		t.Fatalf("the other process said %q, want it to get in after the release", out)
	}
}

// runHelper runs this test binary again, in the helper mode below.
func runHelper(t *testing.T, locks, store string) string {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperTakesTheLock$", "-test.v")
	cmd.Env = append(os.Environ(), "MNEMO_LOCK_HELPER_LOCKS="+locks, "MNEMO_LOCK_HELPER_STORE="+store)
	out, err := cmd.CombinedOutput()
	if err != nil && !strings.Contains(string(out), "HELPER:") {
		t.Fatalf("running the helper: %v\n%s", err, out)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if at := strings.Index(line, "HELPER:"); at != -1 {
			return strings.TrimSpace(line[at+len("HELPER:"):])
		}
	}
	t.Fatalf("the helper said nothing:\n%s", out)
	return ""
}

// startHolder runs this test binary again, in the holder mode below. The channel
// is closed once that process has the lock, and the returned function stops it.
func startHolder(t *testing.T, locks, store string) (<-chan struct{}, func()) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperHoldsTheLock$", "-test.v")
	cmd.Env = append(os.Environ(), "MNEMO_LOCK_HOLDER_LOCKS="+locks, "MNEMO_LOCK_HOLDER_STORE="+store)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	holding := make(chan struct{})
	go func() {
		defer close(holding)
		buf := make([]byte, 4096)
		for {
			n, err := out.Read(buf)
			if n > 0 && strings.Contains(string(buf[:n]), "HOLDER: holding") {
				return
			}
			if err != nil {
				return
			}
		}
	}()

	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}
	t.Cleanup(stop)

	select {
	case <-holding:
	case <-time.After(10 * time.Second):
		stop()
		t.Fatal("the holder process never took the lock")
	}
	return holding, stop
}

// TestHelperHoldsTheLock is not a test. It is a second process that takes the
// lock and keeps it until it is killed, and it does nothing unless a test above
// starts it.
func TestHelperHoldsTheLock(t *testing.T) {
	locks, store := os.Getenv("MNEMO_LOCK_HOLDER_LOCKS"), os.Getenv("MNEMO_LOCK_HOLDER_STORE")
	if locks == "" || store == "" {
		t.Skip("not the holder process")
	}
	release, err := New(locks).Acquire(context.Background(), store)
	if err != nil {
		fmt.Println("HOLDER: failed", err)
		return
	}
	defer release()
	fmt.Println("HOLDER: holding")
	time.Sleep(30 * time.Second) // the test kills this process when it is done
}

// TestHelperTakesTheLock is not a test. It is the second process, and it does
// nothing unless the test above starts it.
func TestHelperTakesTheLock(t *testing.T) {
	locks, store := os.Getenv("MNEMO_LOCK_HELPER_LOCKS"), os.Getenv("MNEMO_LOCK_HELPER_STORE")
	if locks == "" || store == "" {
		t.Skip("not the helper process")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	release, err := New(locks).Acquire(ctx, store)
	if err != nil {
		t.Log("HELPER: timeout")
		return
	}
	release()
	t.Log("HELPER: locked")
}
