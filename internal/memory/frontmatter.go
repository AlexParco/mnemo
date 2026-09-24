package memory

import (
	"regexp"
	"strings"
)

// Value is a frontmatter value: either a scalar or a list.
type Value struct {
	scalar string
	list   []string
	isList bool
}

// Scalar builds a scalar value.
func Scalar(s string) Value { return Value{scalar: s} }

// ListValue builds a list value.
func ListValue(items []string) Value { return Value{list: items, isList: true} }

// IsList reports whether the value was written as a list.
func (v Value) IsList() bool { return v.isList }

// AsScalar returns the scalar, or the empty string for a list.
func (v Value) AsScalar() string {
	if v.isList {
		return ""
	}
	return v.scalar
}

// AsList returns the list, or the scalar as a one-element list.
func (v Value) AsList() []string {
	if v.isList {
		return v.list
	}
	return []string{v.scalar}
}

// Render writes the value back as it appears in a file.
func (v Value) Render() string {
	if v.isList {
		return "[" + strings.Join(v.list, ", ") + "]"
	}
	return v.scalar
}

// Fields are the parsed frontmatter values of one document.
type Fields map[string]Value

// Str returns a key as a scalar: the empty string when it is missing or a list.
func (f Fields) Str(key string) string {
	v, ok := f[key]
	if !ok {
		return ""
	}
	return v.AsScalar()
}

// List returns a key as a list: empty when missing, and a one-element list for a
// scalar. That is the rule the card and the project filter share.
func (f Fields) List(key string) []string {
	v, ok := f[key]
	if !ok {
		return nil
	}
	return v.AsList()
}

// Display returns a key for showing on the card: the fallback when the key is
// missing, a list joined with commas, and otherwise the scalar as written. A key
// that is present but empty stays empty, which is not what Str would give.
func (f Fields) Display(key, fallback string) string {
	v, ok := f[key]
	if !ok {
		return fallback
	}
	if v.isList {
		return strings.Join(v.list, ", ")
	}
	return v.scalar
}

var (
	keyLine   = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_-]*):\s*(.*)$`)
	listValue = regexp.MustCompile(`^\[(.*)\]$`)
)

// ParseFields reads the frontmatter block of a document. A document with no
// block, or with one that is never closed, has no fields.
func ParseFields(text string) Fields {
	fields := Fields{}
	if !strings.HasPrefix(text, "---") {
		return fields
	}
	end := strings.Index(text[3:], "\n---")
	if end == -1 {
		return fields
	}
	for _, raw := range SplitLines(text[3 : end+3]) {
		m := keyLine.FindStringSubmatch(strings.TrimSpace(raw))
		if m == nil {
			continue
		}
		key, val := m[1], strings.TrimSpace(m[2])
		if list := listValue.FindStringSubmatch(val); list != nil {
			var items []string
			for _, item := range strings.Split(list[1], ",") {
				// Whitespace comes off first, then quotes, and nothing after that: a
				// value written as ' a ' keeps its inner spaces.
				if s := StripQuotes(strings.TrimSpace(item)); s != "" {
					items = append(items, s)
				}
			}
			fields[key] = ListValue(items)
			continue
		}
		fields[key] = Scalar(StripQuotes(val))
	}
	return fields
}

// Doc is a document split into its frontmatter and its body, in a way that can be
// written back byte for byte.
type Doc struct {
	// HasFrontmatter is false when the file has no --- block.
	HasFrontmatter bool
	// FM is the raw text between the opening --- and the closing \n---.
	FM string
	// Body is everything from the closing marker onward.
	Body string
	// Fields holds the parsed values.
	Fields Fields
}

// ParseDoc splits a document. Rendering the result gives back the original bytes.
func ParseDoc(text string) Doc {
	if !strings.HasPrefix(text, "---") {
		return Doc{Body: text, Fields: Fields{}}
	}
	end := strings.Index(text[3:], "\n---")
	if end == -1 {
		return Doc{Body: text, Fields: Fields{}}
	}
	end += 3
	return Doc{
		HasFrontmatter: true,
		FM:             text[3:end],
		Body:           text[end+4:],
		Fields:         ParseFields(text),
	}
}

// Render writes the document back.
func (d Doc) Render() string {
	if !d.HasFrontmatter {
		return d.Body
	}
	return "---" + d.FM + "\n---" + d.Body
}

// SetField replaces one key's line, keeping its indentation, its line ending and
// every other byte of the document, and appends the key when it is not there.
// That is what lets a rename rewrite `projects:` without reflowing the note's
// prose. A document with no block gets one, in the body's line ending.
func (d Doc) SetField(key string, value Value) Doc {
	rendered := key + ": " + value.Render()
	fields := Fields{}
	for k, v := range d.Fields {
		fields[k] = v
	}
	fields[key] = value

	if !d.HasFrontmatter {
		eol := lineEnding(d.Body)
		body := d.Body
		if !strings.HasPrefix(body, "\n") && !strings.HasPrefix(body, "\r\n") {
			body = eol + body
		}
		return Doc{HasFrontmatter: true, FM: fmLine(eol, rendered), Body: body, Fields: fields}
	}

	// The value runs to the end of the line but not over a \r, so a CRLF file
	// keeps its ending. No $ anchor: in multi-line mode it would need a \n right
	// after the value, which a \r is not.
	line := regexp.MustCompile(`(?m)^([ \t]*)` + regexp.QuoteMeta(key) + `[ \t]*:[^\r\n]*`)
	fm := d.FM
	if at := line.FindStringSubmatchIndex(fm); at != nil {
		indent := fm[at[2]:at[3]]
		fm = fm[:at[0]] + indent + rendered + fm[at[1]:]
	} else if strings.HasSuffix(fm, "\r") {
		// The block is "\r\nk: v\r\nk: v\r" before its "\n---": open the last line
		// ending, add the line, close it again.
		fm = strings.TrimSuffix(fm, "\r") + fmLine("\r\n", rendered)
	} else {
		fm += fmLine("\n", rendered)
	}
	return Doc{HasFrontmatter: true, FM: fm, Body: d.Body, Fields: fields}
}

// fmLine is one frontmatter line as it sits between the markers: preceded by its
// line ending and, for CRLF, followed by the \r that goes before the "\n---".
func fmLine(eol, line string) string {
	if eol == "\r\n" {
		return "\r\n" + line + "\r"
	}
	return "\n" + line
}

// lineEnding is the ending a document uses: CRLF when it has one, else LF.
func lineEnding(s string) string {
	if strings.Contains(s, "\r\n") {
		return "\r\n"
	}
	return "\n"
}

// WithBody replaces the body and leaves the frontmatter untouched.
func (d Doc) WithBody(body string) Doc {
	d.Body = body
	return d
}
