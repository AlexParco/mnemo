package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Reading a file and writing it back must give the same bytes. Rename and forget
// depend on it: they rewrite one field of a note and must not reflow its prose.
func TestDocRoundTrip(t *testing.T) {
	var files []string
	err := filepath.WalkDir(fixtureStore, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".md") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the fixture store: %v", err)
	}
	if len(files) < 20 {
		t.Fatalf("only %d fixture files found; the walk is not seeing the store", len(files))
	}

	for _, file := range files {
		t.Run(filepath.Base(file), func(t *testing.T) {
			raw, err := os.ReadFile(file)
			if err != nil {
				t.Fatalf("reading: %v", err)
			}
			if got := ParseDoc(string(raw)).Render(); got != string(raw) {
				t.Errorf("the round trip changed the file:\n--- got ---\n%q\n--- want ---\n%q", got, string(raw))
			}
		})
	}
}

func TestDocRoundTripEdgeCases(t *testing.T) {
	cases := map[string]string{
		"CRLF and a rule in the body": "---\r\nid: x\r\nprojects: [a]\r\n---\r\n\r\nBody\r\n\r\n---\r\n\r\nMore\r\n",
		"no frontmatter":              "just a body\n",
		"an unterminated block":       "---\nid: x\nnever closed\n",
		"empty file":                  "",
		"only a marker":               "---\n",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if got := ParseDoc(raw).Render(); got != raw {
				t.Errorf("round trip gave %q, want %q", got, raw)
			}
		})
	}
}

func TestParseFields(t *testing.T) {
	fields := ParseFields(strings.Join([]string{
		"---",
		"projects: [a, 'b', \"c\"]",
		"name: \"Quoted\"",
		"empty:",
		"services: []",
		"scalar: one",
		"  indented: yes",
		"not a key line",
		"# a comment",
		"---",
		"body",
	}, "\n"))

	if got := fields.List("projects"); strings.Join(got, ",") != "a,b,c" {
		t.Errorf("projects = %q, want [a b c]", got)
	}
	if got := fields.Str("name"); got != "Quoted" {
		t.Errorf("name = %q, want Quoted", got)
	}
	if got := fields.Str("empty"); got != "" {
		t.Errorf("empty = %q, want the empty string", got)
	}
	if got := fields.List("services"); len(got) != 0 {
		t.Errorf("services = %q, want no items", got)
	}
	if got := fields.Str("scalar"); got != "one" {
		t.Errorf("scalar = %q, want one", got)
	}
	if got := fields.Str("indented"); got != "yes" {
		t.Errorf("an indented key must still parse, got %q", got)
	}
	if _, ok := fields["not a key line"]; ok {
		t.Error("a line that is not a key must be ignored")
	}
}

func TestFieldAccessors(t *testing.T) {
	fields := ParseFields("---\nprojects: solo\nname:\nlist: [a, b]\n---\n")

	if got := fields.List("projects"); len(got) != 1 || got[0] != "solo" {
		t.Errorf("a scalar must read as a one-element list, got %q", got)
	}
	if got := fields.List("missing"); got != nil {
		t.Errorf("a missing key must read as no list, got %q", got)
	}
	if got := fields.Str("list"); got != "" {
		t.Errorf("a list read as a scalar must be empty, got %q", got)
	}
	// The card needs the difference between absent and present-but-empty: an
	// empty name is shown as empty, a missing one falls back to the slug.
	if got := fields.Display("name", "fallback"); got != "" {
		t.Errorf("a present empty value must stay empty, got %q", got)
	}
	if got := fields.Display("missing", "fallback"); got != "fallback" {
		t.Errorf("a missing value must give the fallback, got %q", got)
	}
	if got := fields.Display("list", ""); got != "a, b" {
		t.Errorf("a list must display joined, got %q", got)
	}
}

func TestParseFieldsWithoutABlock(t *testing.T) {
	for name, text := range map[string]string{
		"no frontmatter":  "just a body\n",
		"never closed":    "---\nid: x\nnever closed\n",
		"marker mid-file": "body\n---\nid: x\n---\n",
		"nothing at all":  "",
	} {
		t.Run(name, func(t *testing.T) {
			if got := ParseFields(text); len(got) != 0 {
				t.Errorf("fields = %v, want none", got)
			}
		})
	}
}

// Written as escapes rather than a raw block: the trailing spaces after the
// projects line are part of the case, and an editor would strip them out of a
// raw literal without anyone noticing.
const setFieldSource = "---\n" +
	"id: note\n" +
	"projects: [alpha, beta]   \n" +
	"  indented: yes\n" +
	"# a comment line the parser ignores\n" +
	"type: decision\n" +
	"---\n" +
	"\n" +
	"Body with projects: [alpha] in the prose.\n"

func TestSetFieldRewritesOneLine(t *testing.T) {
	got := ParseDoc(setFieldSource).SetField("projects", ListValue([]string{"gamma", "beta"})).Render()
	// The rewritten line loses its trailing spaces, and nothing else moves.
	want := strings.Replace(setFieldSource, "projects: [alpha, beta]   \n", "projects: [gamma, beta]\n", 1)
	if got == setFieldSource {
		t.Fatal("the source and the expectation are identical; the replacement did not match")
	}
	if got != want {
		t.Errorf("setting one field changed something else:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
	if !strings.Contains(got, "Body with projects: [alpha] in the prose.") {
		t.Error("the body's prose must survive")
	}
	if !strings.Contains(got, "# a comment line the parser ignores") {
		t.Error("lines the parser does not understand must survive")
	}
}

func TestSetFieldKeepsIndentationAndAppends(t *testing.T) {
	doc := ParseDoc(setFieldSource)

	if got := doc.SetField("indented", Scalar("no")).Render(); !strings.Contains(got, "  indented: no") {
		t.Error("an indented key must keep its indentation")
	}
	got := doc.SetField("updated", Scalar("2026-08-27")).Render()
	if !strings.Contains(got, "type: decision\nupdated: 2026-08-27\n---") {
		t.Errorf("a missing key must be appended at the end of the block, got:\n%s", got)
	}
	if fields := ParseDoc(got).Fields; fields.Str("updated") != "2026-08-27" {
		t.Error("the appended key must parse back")
	}
}

func TestSetFieldOnOtherShapes(t *testing.T) {
	t.Run("a document with no block gets one", func(t *testing.T) {
		// The body is pushed onto its own line: a block that ran into the first
		// word would not parse back as frontmatter.
		got := ParseDoc("Just a body.\n").SetField("id", Scalar("x")).Render()
		want := "---\nid: x\n---\nJust a body.\n"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		if ParseDoc(got).Fields.Str("id") != "x" {
			t.Error("the new block must parse back")
		}
	})

	t.Run("CRLF survives a rewrite", func(t *testing.T) {
		raw := "---\r\nid: x\r\nprojects: [a]\r\n---\r\n\r\nBody\r\n"
		got := ParseDoc(raw).SetField("projects", ListValue([]string{"b"})).Render()
		want := "---\r\nid: x\r\nprojects: [b]\r\n---\r\n\r\nBody\r\n"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("CRLF survives an append", func(t *testing.T) {
		raw := "---\r\nid: x\r\n---\r\n\r\nBody\r\n"
		got := ParseDoc(raw).SetField("type", Scalar("bug")).Render()
		want := "---\r\nid: x\r\ntype: bug\r\n---\r\n\r\nBody\r\n"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		if ParseDoc(got).Fields.Str("type") != "bug" {
			t.Error("the appended key must parse back")
		}
	})

	t.Run("only the first match is rewritten", func(t *testing.T) {
		raw := "---\nid: one\nid: two\n---\n"
		got := ParseDoc(raw).SetField("id", Scalar("three")).Render()
		want := "---\nid: three\nid: two\n---\n"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}

func TestValueRender(t *testing.T) {
	if got := ListValue([]string{"a", "b"}).Render(); got != "[a, b]" {
		t.Errorf("a list renders as %q, want [a, b]", got)
	}
	if got := ListValue(nil).Render(); got != "[]" {
		t.Errorf("an empty list renders as %q, want []", got)
	}
	if got := Scalar("plain").Render(); got != "plain" {
		t.Errorf("a scalar renders as %q, want plain", got)
	}
}
