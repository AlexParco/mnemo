package memory

import (
	"regexp"
	"strings"
)

// Item is one checkbox line of a pending list.
type Item struct {
	Text string
	Done bool
}

// Section is one `##` heading of a pending list with the items under it.
type Section struct {
	// Key is the lower-cased title, used for matching.
	Key string
	// Label is the title as written, used for display.
	Label string
	Items []Item
}

// The core sections feed the card's numbered list and its resume line. They are
// ordered slices, not sets: one pending.md can carry both language variants,
// which is what the union merge of two machines saving in different languages
// produces, and the render must not depend on map order.
var (
	InProgress = []string{"in progress", "en curso"}
	NextUp     = []string{"next", "siguiente"}
)

// IsCoreSection reports whether a section key feeds the numbered list, in which
// case the card does not render it as a block of its own.
func IsCoreSection(key string) bool {
	for _, k := range InProgress {
		if k == key {
			return true
		}
	}
	for _, k := range NextUp {
		if k == key {
			return true
		}
	}
	return false
}

var (
	heading  = regexp.MustCompile(`^##\s+(.*)$`)
	itemLine = regexp.MustCompile(`^\s*-\s*\[( |x|X)\]\s*(.+)$`)
	stamp    = regexp.MustCompile(`\[@([^\]]+)\]`)
)

// ParsePending reads a pending list. Sections are free-form by contract, so this
// keeps every section it finds, in the order it found it, and assumes no fixed
// set. A heading that repeats continues the section it names.
func ParsePending(text string) []Section {
	var sections []Section
	byKey := map[string]int{}
	current := -1
	for _, line := range SplitLines(text) {
		if h := heading.FindStringSubmatch(strings.TrimSpace(line)); h != nil {
			label := strings.TrimSpace(h[1])
			key := strings.ToLower(label)
			if at, seen := byKey[key]; seen {
				current = at
			} else {
				sections = append(sections, Section{Key: key, Label: label})
				current = len(sections) - 1
				byKey[key] = current
			}
			continue
		}
		if current == -1 {
			continue
		}
		if item := itemLine.FindStringSubmatch(line); item != nil {
			sections[current].Items = append(sections[current].Items, Item{
				Text: Truncate(item[2], MaxItemLen),
				Done: strings.EqualFold(item[1], "x"),
			})
		}
	}
	return sections
}

// SectionByKey finds a section, or reports that there is none.
func SectionByKey(sections []Section, key string) (Section, bool) {
	for _, s := range sections {
		if s.Key == key {
			return s, true
		}
	}
	return Section{}, false
}

// OpenItems returns the unchecked item texts of the given section keys, in key
// order.
func OpenItems(sections []Section, keys []string) []string {
	var out []string
	for _, key := range keys {
		section, ok := SectionByKey(sections, key)
		if !ok {
			continue
		}
		for _, item := range section.Items {
			if !item.Done {
				out = append(out, item.Text)
			}
		}
	}
	return out
}

// SplitStamp separates an item's text from its [@machine] stamp, returning the
// text without it and the machine, normalised, or empty when there is no stamp.
//
// The two are kept apart so the card can read as a list of tasks and put the
// machine in its own column, instead of leaving the marker inside the sentence.
// Normalising means a stamp written with different capitalisation or separators
// still names the same machine.
func SplitStamp(text string) (string, string) {
	at := stamp.FindStringSubmatchIndex(text)
	if at == nil {
		return strings.TrimSpace(text), ""
	}
	machine := NormalizeMachine(text[at[2]:at[3]])
	// Join the two sides with one space, so removing a stamp from the middle of a
	// sentence does not run two words together.
	left := strings.TrimRight(text[:at[0]], " \t")
	right := strings.TrimLeft(text[at[1]:], " \t")
	return strings.TrimSpace(left + " " + right), machine
}
