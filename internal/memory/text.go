// Package memory reads a mnemo store: frontmatter, memories, projects, pending
// lists, the overview, search and the resume card. It only reads, and it only
// uses the standard library.
//
// The file format is settled and does not change, so the parsers here describe
// it rather than decide it. The card, by contrast, is this project's own design;
// its layout lives in docs/specs/card.md.
//
// Sorting throughout this package is plain string comparison. Go compares
// strings byte by byte, which for valid UTF-8 is the same order as comparing
// code points, so file names sort the way they read.
package memory

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// MaxItemLen is the budget, in code points, for a pending item or a memory
// summary shown on the card.
const MaxItemLen = 100

// SplitLines breaks on \r\n, \n and \r, and drops the empty element a final
// break would otherwise produce, so "a\n" is one line and not two. Rarer break
// characters, such as the form feed, are ordinary text: a note that contains one
// keeps it inside its line.
func SplitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\n':
			out = append(out, s[start:i])
			start = i + 1
		case '\r':
			out = append(out, s[start:i])
			if i+1 < len(s) && s[i+1] == '\n' {
				i++
			}
			start = i + 1
		}
	}
	out = append(out, s[start:])
	if len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

// StripQuotes removes any run of single and double quotes from both ends, so a
// frontmatter value written as "'a'" reads as a. Quotes inside the value stay.
func StripQuotes(s string) string {
	return strings.Trim(s, "'\"")
}

// Truncate trims the text and, past limit code points, keeps limit-1 of them and
// appends an ellipsis, so the result never exceeds the limit. Counting is by
// code point, so an emoji costs one character of the budget and the card's
// columns line up. A limit below one leaves no room for anything, not even the
// ellipsis.
func Truncate(s string, limit int) string {
	if limit < 1 {
		return ""
	}
	t := strings.TrimSpace(s)
	if utf8.RuneCountInString(t) <= limit {
		return t
	}
	runes := []rune(t)
	return string(runes[:limit-1]) + "…"
}

var notLabel = regexp.MustCompile(`[^a-z0-9-]+`)

// latinFold maps the lower-case Latin letters with diacritics to plain ASCII, so
// that a machine called Ñandú is labelled nandu rather than and. It covers the
// Latin-1 letters plus œ: what a hostname in Spanish or a neighbouring language
// can carry. Anything else outside a-z0-9- still becomes a dash.
var latinFold = map[rune]string{
	'à': "a", 'á': "a", 'â': "a", 'ã': "a", 'ä': "a", 'å': "a", 'æ': "ae",
	'ç': "c",
	'è': "e", 'é': "e", 'ê': "e", 'ë': "e",
	'ì': "i", 'í': "i", 'î': "i", 'ï': "i",
	'ð': "d", 'ñ': "n",
	'ò': "o", 'ó': "o", 'ô': "o", 'õ': "o", 'ö': "o", 'ø': "o", 'œ': "oe",
	'ù': "u", 'ú': "u", 'û': "u", 'ü': "u",
	'ý': "y", 'ÿ': "y", 'þ': "th", 'ß': "ss",
}

// NormalizeMachine turns a machine name into the label used in addresses and in
// pending stamps: lower case, Latin letters with diacritics folded to their base
// letter, every run of other characters becoming a dash, no leading or trailing
// dash, and at most 63 characters. It returns an empty string when nothing is
// left, which every caller that needs a label reports rather than working
// around.
func NormalizeMachine(s string) string {
	var folded strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		if ascii, ok := latinFold[r]; ok {
			folded.WriteString(ascii)
		} else {
			folded.WriteRune(r)
		}
	}
	label := notLabel.ReplaceAllString(folded.String(), "-")
	label = strings.Trim(label, "-")
	if runes := []rune(label); len(runes) > 63 {
		label = strings.Trim(string(runes[:63]), "-")
	}
	return label
}
