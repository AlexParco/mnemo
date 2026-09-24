package memory

import (
	"strings"
	"testing"
)

// Every expected value here is written out by hand: these four functions are the
// ones the rest of the package builds on, so their behaviour is stated, not
// derived from whatever the code happens to do.

func TestSplitLines(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"no break", "a", []string{"a"}},
		{"lf", "a\nb", []string{"a", "b"}},
		{"crlf", "a\r\nb", []string{"a", "b"}},
		{"cr", "a\rb", []string{"a", "b"}},
		{"final break is dropped", "a\n", []string{"a"}},
		{"final crlf is dropped", "a\r\n", []string{"a"}},
		{"only the final empty element is dropped", "a\n\n", []string{"a", ""}},
		{"blank lines inside are kept", "a\n\nb", []string{"a", "", "b"}},
		{"cr then lf apart are two breaks", "a\r\r\nb", []string{"a", "", "b"}},
		{"form feed is not a break", "a\fb", []string{"a\fb"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := SplitLines(c.in)
			if strings.Join(got, "|") != strings.Join(c.want, "|") || len(got) != len(c.want) {
				t.Errorf("SplitLines(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestStripQuotes(t *testing.T) {
	cases := map[string]string{
		`"a"`:     "a",
		`'a'`:     "a",
		`"'a'"`:   "a",
		`a"b`:     `a"b`,
		`"`:       "",
		``:        "",
		` "a" `:   ` "a" `,
		`"a" "b"`: `a" "b`,
	}
	for in, want := range cases {
		if got := StripQuotes(in); got != want {
			t.Errorf("StripQuotes(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTruncate(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		limit int
		want  string
	}{
		{"short text is only trimmed", "  hello  ", 10, "hello"},
		{"exactly the limit is kept", "hello", 5, "hello"},
		{"one past the limit gets the ellipsis", "hello!", 5, "hell…"},
		{"the result never exceeds the limit", "hello world", 3, "he…"},
		{"trimming happens before counting", "   hello   ", 5, "hello"},
		{"counts code points, not bytes", "héllo", 5, "héllo"},
		{"an emoji is one character", "🙂🙂🙂🙂", 4, "🙂🙂🙂🙂"},
		{"cuts between code points", "🙂🙂🙂🙂🙂", 4, "🙂🙂🙂…"},
		{"limit one is just the ellipsis", "hello", 1, "…"},
		{"limit zero is empty", "hello", 0, ""},
		{"a negative limit is empty", "hello", -3, ""},
		{"empty text stays empty", "", 0, ""},
		{"MaxItemLen keeps 99 and the ellipsis", strings.Repeat("x", 150), MaxItemLen, strings.Repeat("x", 99) + "…"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Truncate(c.in, c.limit); got != c.want {
				t.Errorf("Truncate(%q, %d) = %q, want %q", c.in, c.limit, got, c.want)
			}
		})
	}
}

func TestNormalizeMachine(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"already a label", "laptop-1", "laptop-1"},
		{"lower-cases", "Laptop", "laptop"},
		{"padding is dropped", "  laptop  ", "laptop"},
		{"a run of other characters is one dash", "my laptop_1", "my-laptop-1"},
		{"dots are dashes", "mac.local", "mac-local"},
		{"leading and trailing dashes are dropped", "--laptop--", "laptop"},
		{"spanish hostname keeps its letters", "Ñandú", "nandu"},
		{"every spanish diacritic folds", "áéíóúüñ", "aeiouun"},
		{"a folded letter does not split the word", "Ñandú-MacBook.local", "nandu-macbook-local"},
		{"ligatures and sharp s expand", "Æøß", "aeoss"},
		{"a letter outside latin still becomes a dash", "ダ-laptop", "laptop"},
		{"only symbols gives nothing", "---", ""},
		{"empty gives nothing", "", ""},
		{"cut at 63 characters", strings.Repeat("a", 70), strings.Repeat("a", 63)},
		{"the cut leaves no trailing dash", strings.Repeat("a", 62) + "-b", strings.Repeat("a", 62)},
		{"the cut counts code points", strings.Repeat("é", 70), strings.Repeat("e", 63)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := NormalizeMachine(c.in); got != c.want {
				t.Errorf("NormalizeMachine(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
