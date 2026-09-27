package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/AlexParco/mnemo/internal/gitx"
)

// Talking to the hub: pulling what the other machines saved, resolving what
// collided, and publishing.
//
// Each one holds the store's lock for the same reason a write does. Git has one
// index and one HEAD, so a pull that rebases while another agent is committing
// leaves a half-applied state that neither of them asked for.
//
// The work itself is in gitx. What these add is the lock, and provisioning the
// store first: a machine that has never saved can still sync, and what it gets
// is the hub's memory.

// Sync brings in what the other machines saved.
func (s *Store) Sync(ctx context.Context) (gitx.SyncResult, error) {
	var result gitx.SyncResult
	err := s.hold(ctx, func() error {
		if _, err := s.ensure(); err != nil {
			return err
		}
		result = s.repo.With(ctx).Pull()
		return nil
	})
	return result, err
}

// ResolveConflict writes the merged version of one conflicted file and stages
// it, and reports what is still unresolved.
//
// The path is validated here rather than trusted: it arrives from a tool call,
// and a path that climbs out of the store would have git stage a file that is
// not memory at all.
func (s *Store) ResolveConflict(ctx context.Context, file, content string) ([]string, error) {
	var remaining []string
	err := s.hold(ctx, func() error {
		// The write-time wording is wrong here. A merged file carries content
		// from the hub, which is already saved on another machine, so "keep the
		// value where secrets belong and save a note instead" reads as an
		// instruction to delete a memory in order to make the call succeed.
		if findings := newFindings(s.Dir, file, content); len(findings) > 0 {
			return refuse("'%s' was not written: line %d of the merged text matches %s. That text may have come "+
				"in from the hub, in which case it is already saved on another machine — deleting it here would "+
				"destroy a memory, not protect a secret. Do NOT remove the line to make this call succeed. Stop "+
				"and tell the user which line and which rule, never the value: they can edit the file themselves "+
				"and then have you call mnemo_rebase \"continue\", or have you call mnemo_rebase \"abort\" to "+
				"put the store back as it was before the sync.", file, findings[0].Line, findings[0].Rule)
		}
		var err error
		remaining, err = s.repo.ResolveConflict(file, content)
		switch {
		case errors.Is(err, gitx.ErrOutsideRepo):
			return refuse("'%s' is not inside the store. Use the path mnemo_sync reported, exactly as it "+
				"reported it. Nothing was written.", file)
		case errors.Is(err, gitx.ErrNotConflicted):
			return refuse("'%s' is not one of the files a sync left conflicted. This tool is only for those: "+
				"call mnemo_sync to see which they are. Nothing was written. To change a memory, use "+
				"mnemo_write_memory; to change a pending list, mnemo_write_pending.", file)
		case errors.Is(err, gitx.ErrConflictMarkers):
			// Staging this would commit the markers into the user's memory,
			// where the next session would read them as content.
			return refuse("'%s' still contains conflict markers. Send the merged file with <<<<<<<, ======= and "+
				">>>>>>> gone and the content of BOTH sides kept. Nothing was written.", file)
		}
		return err
	})
	return remaining, err
}

// newFindings are the secrets a merge would introduce, which is not the same as
// the secrets it contains.
//
// A conflicted file on disk holds both sides of the collision, so anything
// already in it came from a commit — on this machine or on the hub — and is
// already in the user's memory. Refusing the merge for one of those would leave
// the machine unable to finish the rebase and unable to ever pull again, with no
// way out but abort. What matters is a value the agent is adding now.
func newFindings(dir, file, merged string) []gitx.Finding {
	before, err := os.ReadFile(filepath.Join(dir, filepath.Clean(file)))
	if err != nil {
		return gitx.ScanContent(file, merged)
	}
	existing := map[string]bool{}
	for _, f := range gitx.ScanContent(file, string(before)) {
		existing[f.Rule+"\x00"+f.Excerpt] = true
	}
	var added []gitx.Finding
	for _, f := range gitx.ScanContent(file, merged) {
		if !existing[f.Rule+"\x00"+f.Excerpt] {
			added = append(added, f)
		}
	}
	return added
}

// Rebase finishes what a sync started, or puts the store back as it was.
func (s *Store) Rebase(ctx context.Context, action gitx.RebaseAction) (gitx.RebaseResult, error) {
	var result gitx.RebaseResult
	err := s.hold(ctx, func() error {
		result = s.repo.Rebase(action)
		return nil
	})
	return result, err
}

// Push publishes local commits, after the secret scan that has no way around it.
func (s *Store) Push(ctx context.Context, acknowledge string) (gitx.PushResult, error) {
	var result gitx.PushResult
	err := s.hold(ctx, func() error {
		result = s.repo.With(ctx).Push(acknowledge)
		return nil
	})
	return result, err
}
