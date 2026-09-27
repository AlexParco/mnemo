package gitx

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Syncing with the hub: pull, conflicts, rebase, push.
//
// Merging two memories is reading, not pattern matching, so resolving a conflict
// stays with the agent. The git mechanics do not, and neither does the secret
// scan: there is no path from here to the network that goes around it.

// A Conflict is a file a merge left for someone to resolve, as git left it.
type Conflict struct {
	File string
	// Content is the file with its conflict markers still in it.
	Content string
}

// SyncResult is what a pull did, or why it did nothing.
type SyncResult struct {
	HasRemote bool
	// Pulled is true when the pull ran and left the store clean.
	Pulled    bool
	Conflicts []Conflict
	Rebasing  bool
	Detail    string
}

// ReadConflicts lists the conflicted files with their contents.
func (r *Repo) ReadConflicts() []Conflict {
	var conflicts []Conflict
	for _, file := range r.ConflictedFiles() {
		content, err := os.ReadFile(filepath.Join(r.Dir, file))
		if err != nil {
			continue
		}
		conflicts = append(conflicts, Conflict{File: file, Content: string(content)})
	}
	return conflicts
}

// Pull brings in what the user saved on their other machines.
func (r *Repo) Pull() SyncResult {
	status := r.Status()
	if !status.HasRemote() {
		return SyncResult{Detail: "This store has no remote; there is nothing to sync with."}
	}
	if status.Rebasing {
		return SyncResult{
			HasRemote: true,
			Conflicts: r.ReadConflicts(),
			Rebasing:  true,
			Detail:    "A rebase was already in progress: finish it before syncing again.",
		}
	}
	// Without a tracking branch there is nothing to rebase onto, and guessing one
	// could merge two unrelated memories into each other.
	if _, ok := r.Upstream(); !ok {
		return SyncResult{
			HasRemote: true,
			Detail:    "The remote is wired but this branch tracks nothing yet; the first push will set that up.",
		}
	}

	reaching, done := r.reaching()
	pull := reaching.Attempt("pull", "--rebase", "--autostash")
	done()
	conflicts := r.ReadConflicts()
	if pull.OK && len(conflicts) == 0 {
		detail := pull.Stdout
		if detail == "" {
			detail = "Already up to date."
		}
		return SyncResult{HasRemote: true, Pulled: true, Detail: detail}
	}
	detail := pull.Detail()
	if detail == "" {
		detail = "The pull did not complete."
	}
	return SyncResult{HasRemote: true, Conflicts: conflicts, Rebasing: r.Status().Rebasing, Detail: detail}
}

// ErrOutsideRepo is a path that would write outside the store.
var ErrOutsideRepo = errors.New("the path is outside the store")

// ErrNotConflicted is a file that no sync left conflicted. Resolving is only for
// the files a merge stopped on; anything else would be a whole-file overwrite of
// the user's memory with no confirmation and no validation.
var ErrNotConflicted = errors.New("the file is not conflicted")

// ErrConflictMarkers is content that still carries `<<<<<<<` and its companions.
// A half-merged memory committed as resolved is worse than a conflict: the
// conflict is visible, and the bad merge is not.
var ErrConflictMarkers = errors.New("the content still contains conflict markers")

var markerLine = regexp.MustCompile(`(?m)^(<<<<<<<|=======|>>>>>>>)`)

// ResolveConflict writes the merged version of a conflicted file and stages it.
// It returns the files still left to resolve.
func (r *Repo) ResolveConflict(file, content string) ([]string, error) {
	target := filepath.Join(r.Dir, filepath.Clean(file))
	rel, err := filepath.Rel(r.Dir, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("%w: %s", ErrOutsideRepo, file)
	}
	if markerLine.MatchString(content) {
		return nil, fmt.Errorf("%w: %s", ErrConflictMarkers, file)
	}
	if !slices.Contains(r.ConflictedFiles(), rel) {
		return nil, fmt.Errorf("%w: %s", ErrNotConflicted, file)
	}
	// A symlink defeats the check above: the path stays inside the store while
	// the write follows the link out of it. Git tracks symlinks, so one can
	// arrive from the hub.
	if resolved, err := filepath.EvalSymlinks(filepath.Dir(target)); err == nil {
		inside, relErr := filepath.Rel(r.Dir, resolved)
		if relErr != nil || inside == ".." || strings.HasPrefix(inside, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("%w: %s", ErrOutsideRepo, file)
		}
	}

	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
		return nil, fmt.Errorf("writing the merged file: %w", err)
	}
	if err := r.Stage(rel); err != nil {
		return nil, err
	}
	return r.ConflictedFiles(), nil
}

// RebaseAction is what to do with a rebase that a pull started.
type RebaseAction string

const (
	Continue RebaseAction = "continue"
	Abort    RebaseAction = "abort"
)

// RebaseResult is how finishing the rebase went.
type RebaseResult struct {
	Action    RebaseAction
	OK        bool
	Rebasing  bool
	Remaining []string
	Detail    string
}

// Rebase finishes the rebase a pull started, or puts the store back as it was.
func (r *Repo) Rebase(action RebaseAction) RebaseResult {
	if !r.Status().Rebasing {
		return RebaseResult{Action: action, Detail: "No rebase is in progress."}
	}
	if remaining := r.ConflictedFiles(); action == Continue && len(remaining) > 0 {
		return RebaseResult{
			Action:    action,
			Rebasing:  true,
			Remaining: remaining,
			Detail:    "Still unresolved: " + strings.Join(remaining, ", ") + ". Resolve them before continuing.",
		}
	}

	res := r.Attempt("rebase", "--"+string(action))
	detail := res.Detail()
	if detail == "" {
		detail = map[RebaseAction]string{
			Continue: "Rebase continued.",
			Abort:    "Rebase aborted; the store is as it was.",
		}[action]
	}
	return RebaseResult{
		Action:    action,
		OK:        res.OK,
		Rebasing:  r.Status().Rebasing,
		Remaining: r.ConflictedFiles(),
		Detail:    detail,
	}
}

// PushRefusal says why a push did not happen. An empty value means it did.
type PushRefusal string

const (
	RefusedNoRemote       PushRefusal = "no-remote"
	RefusedRebasing       PushRefusal = "rebase-in-progress"
	RefusedNothingToPush  PushRefusal = "nothing-to-push"
	RefusedSecrets        PushRefusal = "secrets"
	RefusedBadAcknowledge PushRefusal = "bad-acknowledgement"
)

// PushResult is what a push did, or why it refused.
type PushResult struct {
	Pushed   bool
	Refused  PushRefusal
	Findings []Finding
	// Acknowledge is the value that would let this exact set of findings through.
	Acknowledge string
	// Commits counts what would be, or was, published.
	Commits int
	Detail  string
}

// Push publishes local commits, after scanning what they would publish.
//
// A refusal is a normal result, not an error: every one of them is something the
// user can act on, and the agent has to be able to show it.
func (r *Repo) Push(acknowledge string) PushResult {
	status := r.Status()
	if !status.HasRemote() {
		return PushResult{Refused: RefusedNoRemote, Detail: "This store has no remote, so it lives only on this machine."}
	}
	if status.Rebasing {
		return PushResult{Refused: RefusedRebasing, Detail: "A rebase is in progress; finish or abort it before pushing."}
	}

	base, ok := r.PushBase()
	if !ok {
		return PushResult{Refused: RefusedNothingToPush, Detail: "The store has no commits yet."}
	}
	commits := 0
	if count, ok := r.Try("rev-list", "--count", base+"..HEAD"); ok {
		commits, _ = strconv.Atoi(count)
	}
	if commits == 0 {
		return PushResult{Refused: RefusedNothingToPush, Detail: "Everything is already on the hub."}
	}

	// The scan is not a step that can be skipped: this is the only way out.
	if findings := r.ScanRange(); len(findings) > 0 {
		token := r.AcknowledgeToken(findings)
		if acknowledge != token {
			refused := RefusedSecrets
			detail := fmt.Sprintf("%d possible secret(s) in what would be published. Nothing was pushed.", len(findings))
			if acknowledge != "" {
				refused = RefusedBadAcknowledge
				detail = "That acknowledgement does not match these findings: they changed, or it was not the one issued. Nothing was pushed."
			}
			return PushResult{Refused: refused, Findings: findings, Acknowledge: token, Commits: commits, Detail: detail}
		}
	}

	reaching, done := r.reaching()
	defer done()
	res := reaching.Attempt("push", "-q")
	if _, hasUpstream := r.Upstream(); !hasUpstream {
		// Nothing tracks this branch yet, which is every store's first push. The
		// push sets the tracking up, so the next one needs no arguments.
		branch := status.Branch
		if branch == "" {
			branch = "main"
		}
		res = reaching.Attempt("push", "-q", "-u", "origin", branch)
	}
	if !res.OK {
		return PushResult{Commits: commits, Detail: "git push failed: " + res.Detail()}
	}
	return PushResult{
		Pushed:  true,
		Commits: commits,
		Detail:  fmt.Sprintf("Pushed %d commit(s) to the hub.", commits),
	}
}
