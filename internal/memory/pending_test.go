package memory

import (
	"strings"
	"testing"
)

// pending.md is free-form by contract: the card renders whatever sections exist.
// The parser therefore preserves, and never normalises.

func TestParsePendingKeepsSectionsInFileOrder(t *testing.T) {
	sections := ParsePending("## Next\n- [ ] a\n\n## Wild Section\n- [x] b\n\n## Blocked\n- [ ] c\n")
	var labels []string
	for _, s := range sections {
		labels = append(labels, s.Label)
	}
	if strings.Join(labels, ",") != "Next,Wild Section,Blocked" {
		t.Errorf("labels = %q, want the file's order", labels)
	}
}

func TestParsePendingItems(t *testing.T) {
	sections := ParsePending("## Next\n- [ ] open\n- [x] closed\n- [X] also closed\n- plain bullet\n- [-] not a checkbox\n")
	if len(sections) != 1 {
		t.Fatalf("got %d sections, want 1", len(sections))
	}
	want := []Item{
		{Text: "open", Done: false},
		{Text: "closed", Done: true},
		{Text: "also closed", Done: true},
	}
	got := sections[0].Items
	if len(got) != len(want) {
		t.Fatalf("items = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("item %d = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestParsePendingEdges(t *testing.T) {
	t.Run("items before any heading are dropped", func(t *testing.T) {
		sections := ParsePending("- [ ] orphan\n## Next\n- [ ] kept\n")
		if len(sections) != 1 || len(sections[0].Items) != 1 || sections[0].Items[0].Text != "kept" {
			t.Errorf("sections = %v, want only the item under the heading", sections)
		}
	})

	t.Run("a repeated heading continues its section", func(t *testing.T) {
		sections := ParsePending("## Next\n- [ ] a\n\n## Other\n\n## next\n- [ ] b\n")
		if len(sections) != 2 {
			t.Fatalf("got %d sections, want 2", len(sections))
		}
		if sections[0].Label != "Next" || len(sections[0].Items) != 2 {
			t.Errorf("first section = %v, want Next with both items", sections[0])
		}
	})

	t.Run("a long item is truncated when it is parsed", func(t *testing.T) {
		long := strings.Repeat("x", 150)
		sections := ParsePending("## Next\n- [ ] " + long + "\n")
		if got := sections[0].Items[0].Text; got != strings.Repeat("x", 99)+"…" {
			t.Errorf("item text is %d characters, want the parse-level budget", len([]rune(got)))
		}
	})

	t.Run("an empty list has no sections", func(t *testing.T) {
		if got := ParsePending(""); len(got) != 0 {
			t.Errorf("sections = %v, want none", got)
		}
	})
}

func TestCoreSections(t *testing.T) {
	// One pending.md can carry both language variants: that is what the union
	// merge of two machines saving in different languages produces. The order
	// must not depend on map iteration, so it is stated here.
	mixed := "## En curso\n- [ ] spanish\n\n## In progress\n- [ ] english\n"
	for i := 0; i < 20; i++ {
		got := OpenItems(ParsePending(mixed), InProgress)
		if strings.Join(got, ",") != "english,spanish" {
			t.Fatalf("run %d gave %q, want English first, then Spanish", i, got)
		}
	}

	if strings.Join(InProgress, ",") != "in progress,en curso" {
		t.Errorf("InProgress = %q, want English first", InProgress)
	}
	if strings.Join(NextUp, ",") != "next,siguiente" {
		t.Errorf("NextUp = %q, want English first", NextUp)
	}
	for _, key := range append(append([]string{}, InProgress...), NextUp...) {
		if !IsCoreSection(key) {
			t.Errorf("%q must count as a core section, or the card renders it twice", key)
		}
	}
	if IsCoreSection("blocked") {
		t.Error("a free-form section must not count as core")
	}
}

func TestOpenItemsSkipDone(t *testing.T) {
	got := OpenItems(ParsePending("## Next\n- [x] done\n- [ ] open\n"), NextUp)
	if strings.Join(got, ",") != "open" {
		t.Errorf("open items = %q, want only the unchecked one", got)
	}
	if got := OpenItems(ParsePending("## Next\n- [ ] a\n"), []string{"missing"}); got != nil {
		t.Errorf("a section that does not exist gives %q, want nothing", got)
	}
}

func TestSplitStamp(t *testing.T) {
	cases := []struct {
		name        string
		in          string
		wantText    string
		wantMachine string
	}{
		{"no stamp", "push the repo", "push the repo", ""},
		{"at the end", "push the repo [@laptop]", "push the repo", "laptop"},
		{"padding inside the stamp", "push the repo [@ laptop ]", "push the repo", "laptop"},
		{"in the middle", "push [@laptop] the repo", "push the repo", "laptop"},
		{"at the start", "[@laptop] push the repo", "push the repo", "laptop"},
		{"the machine is normalised", "push the repo [@My_Laptop]", "push the repo", "my-laptop"},
		{"only the first stamp is taken", "a [@one] b [@two]", "a b [@two]", "one"},
		{"nothing but a stamp", "[@laptop]", "", "laptop"},
		{"surrounding spaces go", "  push the repo  ", "push the repo", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			text, machine := SplitStamp(c.in)
			if text != c.wantText || machine != c.wantMachine {
				t.Errorf("SplitStamp(%q) = (%q, %q), want (%q, %q)", c.in, text, machine, c.wantText, c.wantMachine)
			}
		})
	}
}
