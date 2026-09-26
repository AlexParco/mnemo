// Package criterion holds the texts an agent reads: what is worth saving, what
// belongs in a pending list, how machine-bound work is treated, how to merge a
// conflict, and when to stop and ask a person.
//
// These are the rules no tool can enforce. mnemo can refuse a bad write, but it
// cannot decide that a fact is worth keeping, and it cannot see the conversation
// the agent is in. So the rules travel as text, and they reach an agent through
// three channels that not every client supports:
//
//  1. the server's instructions field, which is base protocol and the only one
//     that reaches a client with neither prompts nor resources;
//  2. MCP prompts, which some clients surface;
//  3. a snippet the user pastes into AGENTS.md or CLAUDE.md, from mnemo_guide.
//
// Every text is used by at least two of them, which is why they live here once
// instead of being written out at each place that sends them. The plugin's
// skills carry copies, and a test compares those copies to these originals.
//
// The texts themselves are the files in rules/, one per name. They are data, not
// code: a person edits them without reading Go, and what ships is byte for byte
// what the file says.
package criterion

import (
	"embed"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"
)

//go:embed rules/*.md
var files embed.FS

// The rule texts, filled at startup from rules/.
var (
	ServerInstructions string
	SaveCriterion      string
	PendingCriterion   string
	MachineRule        string
	ConflictRule       string
	ConfirmNote        string
	ConfirmationRule   string
	MailboxRule        string
	// Guide is the whole criterion as a rules file, built from the texts above.
	Guide string
)

// bound maps each name to the variable that carries its text. Every file in
// rules/ has to be bound and every binding has to have a file, so a text that
// is added with no way to reach it, or a variable with no text behind it, stops
// the program at startup instead of shipping empty.
var bound = map[string]*string{
	"SERVER_INSTRUCTIONS": &ServerInstructions,
	"SAVE_CRITERION":      &SaveCriterion,
	"PENDING_CRITERION":   &PendingCriterion,
	"MACHINE_RULE":        &MachineRule,
	"CONFLICT_RULE":       &ConflictRule,
	"CONFIRM_NOTE":        &ConfirmNote,
	"CONFIRMATION_RULE":   &ConfirmationRule,
	"MAILBOX_RULE":        &MailboxRule,
	"GUIDE":               &Guide,
}

// inclusion is how one text carries another: `{{NAME}}`, replaced by that text.
// CONFIRMATION_RULE includes CONFIRM_NOTE this way, and the guide includes
// nearly everything, so the short form a tool returns and the long form in a
// rules file cannot drift into saying slightly different things.
var inclusion = regexp.MustCompile(`\{\{([A-Z_]+)\}\}`)

func init() {
	raw, err := read(files)
	if err != nil {
		panic("criterion: " + err.Error())
	}
	for name := range raw {
		text, err := expand(name, raw, nil)
		if err != nil {
			panic("criterion: " + err.Error())
		}
		*bound[name] = text
	}
}

// read loads every file in rules/ and checks it against the bindings. It takes
// the filesystem so a test can hand it a rules/ that is wrong on purpose.
func read(fsys fs.FS) (map[string]string, error) {
	entries, err := fs.ReadDir(fsys, "rules")
	if err != nil {
		return nil, err
	}
	raw := map[string]string{}
	for _, e := range entries {
		name := nameOf(e.Name())
		if _, ok := bound[name]; !ok {
			return nil, fmt.Errorf("rules/%s has no variable bound to it: add %s to bound", e.Name(), name)
		}
		content, err := fs.ReadFile(fsys, "rules/"+e.Name())
		if err != nil {
			return nil, err
		}
		// A text is a block, not a file: the editor's trailing newline is not
		// part of it, so a text can be dropped into a sentence or a table cell.
		raw[name] = strings.TrimRight(string(content), "\n")
	}
	var missing []string
	for name := range bound {
		if _, ok := raw[name]; !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		files := make([]string, len(missing))
		for i, name := range missing {
			files[i] = "rules/" + fileOf(name) + ".md"
		}
		return nil, fmt.Errorf("no text for %s: add %s", strings.Join(missing, ", "), strings.Join(files, ", "))
	}
	return raw, nil
}

// expand resolves the inclusions of one text, refusing a cycle rather than
// looping until the stack runs out.
func expand(name string, raw map[string]string, open []string) (string, error) {
	for _, already := range open {
		if already == name {
			// The text that closes the loop is this one, not the one the walk
			// started from: naming open[0] would send a person to the wrong file.
			return "", fmt.Errorf("%s includes itself through %s", name, strings.Join(append(open, name), " → "))
		}
	}
	open = append(open, name)

	var failure error
	out := inclusion.ReplaceAllStringFunc(raw[name], func(match string) string {
		included := inclusion.FindStringSubmatch(match)[1]
		if _, ok := raw[included]; !ok {
			failure = fmt.Errorf("%s includes %s, which is not a rule text", name, included)
			return match
		}
		text, err := expand(included, raw, open)
		if err != nil {
			failure = err
			return match
		}
		return text
	})
	if failure != nil {
		return "", failure
	}
	return out, nil
}

// nameOf turns a file name into a rule name: save-criterion.md → SAVE_CRITERION.
func nameOf(file string) string {
	return strings.ToUpper(strings.ReplaceAll(strings.TrimSuffix(file, ".md"), "-", "_"))
}

// fileOf is the inverse, for error messages that say which file to write.
func fileOf(name string) string {
	return strings.ToLower(strings.ReplaceAll(name, "_", "-"))
}

// Names lists every rule text, sorted, for the tests and for the skill check.
func Names() []string {
	out := make([]string, 0, len(bound))
	for name := range bound {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Text returns a rule text by name, as the skill check needs it: the names it
// finds in the files come from the files, not from this package.
func Text(name string) (string, bool) {
	held, ok := bound[name]
	if !ok {
		return "", false
	}
	return *held, true
}
