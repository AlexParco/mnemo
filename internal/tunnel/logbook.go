package tunnel

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// maxLog is how big the log is allowed to get before it is rolled over. The
// keeper can run for months and ssh can be made to say a great deal, so the log
// is capped at two files: a machine nobody is watching must not fill its disk
// with the story of a tunnel that was fine.
const maxLog = 1 << 20

// A logbook is the keeper's log file: one line per thing that happened, plus
// everything ssh had to say, rolled over at a mebibyte.
//
// It is an io.Writer because ssh's own output goes through it, which is also why
// it is guarded: the keeper's lines and the two goroutines os/exec uses to copy
// ssh's streams all write to the same file.
type logbook struct {
	path string

	mu   sync.Mutex
	file *os.File
	size int64
}

// openLogbook opens the log for appending, creating its directory.
func openLogbook(path string) (*logbook, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("creating the log directory %s: %w", filepath.Dir(path), err)
	}
	book := &logbook{path: path}
	if err := book.open(); err != nil {
		return nil, err
	}
	return book, nil
}

func (l *logbook) open() error {
	file, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("opening the tunnel log %s: %w", l.path, err)
	}
	size := int64(0)
	if info, err := file.Stat(); err == nil {
		size = info.Size()
	}
	l.file, l.size = file, size
	return nil
}

// Write appends, and rolls the file over once it has passed the limit.
//
// The check is after the write rather than before it, so a line is never split
// across the two files: a log is read by a person looking for ssh's last words,
// and half a sentence at the end of one file is worse than a file slightly over
// the limit.
func (l *logbook) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	written, err := l.file.Write(p)
	l.size += int64(written)
	if err != nil {
		return written, err
	}
	if l.size > maxLog {
		return written, l.rotate()
	}
	return written, nil
}

// say writes one of the keeper's own lines, stamped so that two outages a week
// apart can be told apart.
//
// Failing to write it is not reported: the keeper's job is to hold the tunnel
// open, and a full disk is not a reason to let it drop.
func (l *logbook) say(at time.Time, format string, args ...any) {
	_, _ = fmt.Fprintf(l, "%s %s\n", at.UTC().Format(time.RFC3339), fmt.Sprintf(format, args...))
}

// rotate keeps the previous mebibyte and nothing older. Called with the lock
// held.
func (l *logbook) rotate() error {
	if err := l.file.Close(); err != nil {
		return err
	}
	// Any earlier rolled-over file is replaced. Keeping a chain of them would
	// mean deciding when to delete them, and two files are enough to see what
	// happened: the current outage and the one before it.
	if err := os.Rename(l.path, l.path+".1"); err != nil {
		// The log has to keep working even if the rename could not happen, so the
		// same file is reopened and the next write tries again.
		if reopened := l.open(); reopened != nil {
			return reopened
		}
		return fmt.Errorf("rolling over the tunnel log: %w", err)
	}
	return l.open()
}

// Close releases the file. The keeper's own exit is what ends the log.
func (l *logbook) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.file.Close()
}
