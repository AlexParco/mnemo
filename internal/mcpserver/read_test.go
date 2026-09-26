package mcpserver

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AlexParco/mnemo/internal/memory"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// project writes a store with one project, its pending list and its memories.
func project(t *testing.T, pending string, memories ...string) *Call {
	t.Helper()
	dir := t.TempDir()
	for _, sub := range []string{filepath.Join("projects", "shop"), "memories", "shared"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	put(t, filepath.Join(dir, "projects", "shop", "INDEX.md"),
		"---\nslug: shop\nname: Shop\nstatus: active\nupdated: 2026-01-02\n---\n\nThe shop.\n")
	if pending != "" {
		put(t, filepath.Join(dir, "projects", "shop", "pending.md"), pending)
	}
	for i, body := range memories {
		put(t, filepath.Join(dir, "memories", fmt.Sprintf("fact-%d.md", i)),
			fmt.Sprintf("---\nid: fact-%d\ntype: decision\nprojects: [shop]\nupdated: 2026-01-02\n---\n\n%s\n", i, body))
	}
	return &Call{StoreDir: dir, Machine: "here", Lang: memory.EN}
}

func put(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// blocks runs a handler and returns its text blocks, failing on a refusal.
func blocks(t *testing.T, handle Handler, call *Call, args Args) []string {
	t.Helper()
	result := handle(t.Context(), call, args).result()
	if result.IsError {
		t.Fatalf("refused: %s", textOf(t, result))
	}
	return textOf(t, result)
}

func textOf(t *testing.T, result *mcp.CallToolResult) []string {
	t.Helper()
	var out []string
	for _, content := range result.Content {
		text, ok := content.(*mcp.TextContent)
		if !ok {
			t.Fatalf("a block is %T, not text", content)
		}
		out = append(out, text.Text)
	}
	return out
}

// Finished work is never deleted from a pending list, so the list grows without
// limit and the answer is what has to be bounded.
func TestTheAnswerIsBoundedAndTheFileIsNot(t *testing.T) {
	var lines []string
	for i := 0; i < 30; i++ {
		lines = append(lines, fmt.Sprintf("- [x] finished %d", i))
	}
	call := project(t, "## In progress\n\n- [ ] still going\n\n## Done\n\n"+strings.Join(lines, "\n")+"\n")

	got := blocks(t, handleLoadProject, call, Args{"slug": "shop"})
	if len(got) != 4 {
		t.Fatalf("got %d blocks, want the card, the detail, what was left out and the machine block: %q", len(got), got)
	}

	_, encoded, found := strings.Cut(got[1], "\n")
	if !found {
		t.Fatalf("the detail block has no JSON:\n%s", got[1])
	}
	var body detail
	if err := json.Unmarshal([]byte(encoded), &body); err != nil {
		t.Fatalf("the detail is not JSON: %v", err)
	}
	open, done := 0, 0
	for _, section := range body.Pending {
		for _, item := range section.Items {
			if item.Done {
				done++
			} else {
				open++
			}
		}
	}
	if open != 1 {
		t.Errorf("%d unchecked items came back; every one of them must, they are the work", open)
	}
	if done != doneBudget {
		t.Errorf("%d ticked items came back, want %d", done, doneBudget)
	}
	// The most recent are the ones an agent is about to need.
	if !strings.Contains(got[1], "finished 29") || strings.Contains(got[1], "finished 0\"") {
		t.Error("the ticked items that came back are not the most recent ones")
	}

	if !strings.Contains(got[2], "10 older ticked item(s)") {
		t.Errorf("nothing says what was left out:\n%s", got[2])
	}
	if !strings.Contains(got[2], "Nothing was deleted") {
		t.Errorf("the answer does not say the file still has them:\n%s", got[2])
	}
}

// The first block is the card, and an agent is told to print that one and
// nothing else. Anything that reorders the blocks breaks that instruction.
func TestTheCardComesFirst(t *testing.T) {
	call := project(t, "## In progress\n\n- [ ] wire the parser\n", "A decision.")

	got := blocks(t, handleLoadProject, call, Args{"slug": "shop"})
	if !strings.HasPrefix(got[0], "shop — Shop") {
		t.Errorf("the first block is not the card:\n%s", got[0])
	}
	if strings.Contains(got[0], "{") {
		t.Errorf("the card block carries JSON:\n%s", got[0])
	}
	if !strings.HasPrefix(got[1], "Detail (do not print unless asked):\n{") {
		t.Errorf("the second block does not label its JSON:\n%s", got[1])
	}

	// Asking for the card alone leaves the detail out entirely.
	short := blocks(t, handleLoadProject, call, Args{"slug": "shop", "detail": "card"})
	if len(short) != 2 || short[0] != got[0] {
		t.Errorf("detail=card returned %d blocks", len(short))
	}
}

func TestLoadRefusesWhatItCannotFind(t *testing.T) {
	call := project(t, "")

	t.Run("an unknown slug names the real ones", func(t *testing.T) {
		result := handleLoadProject(t.Context(), call, Args{"slug": "shopp"}).result()
		if !result.IsError {
			t.Fatal("an unknown slug was accepted")
		}
		text := textOf(t, result)[0]
		for _, want := range []string{"No project 'shopp'", "Existing slugs: shop", "do not guess"} {
			if !strings.Contains(text, want) {
				t.Errorf("the refusal does not say %q:\n%s", want, text)
			}
		}
	})

	t.Run("no store at all is a different answer from an empty one", func(t *testing.T) {
		missing := &Call{StoreDir: filepath.Join(t.TempDir(), "nothing"), Machine: "here"}
		result := handleLoadProject(t.Context(), missing, Args{"slug": "shop"}).result()
		if !result.IsError {
			t.Fatal("a missing store was accepted")
		}
		if text := textOf(t, result)[0]; !strings.Contains(text, "There is no mnemo store yet") {
			t.Errorf("the refusal reads:\n%s", text)
		}
	})
}

// An empty search must stop the agent answering from its own recollection.
func TestAnEmptySearchSaysSoRatherThanNothing(t *testing.T) {
	call := project(t, "", "Something else entirely.")

	got := blocks(t, handleSearch, call, Args{"query": "nothing like this"})
	if len(got) != 1 {
		t.Fatalf("got %d blocks", len(got))
	}
	for _, want := range []string{"No memories matched", "rather than filling the gap", "widen it"} {
		if !strings.Contains(got[0], want) {
			t.Errorf("the answer does not say %q:\n%s", want, got[0])
		}
	}
}

// A result that was cut must never look like a complete one.
func TestACutSearchSaysItWasCut(t *testing.T) {
	call := project(t, "", "alpha one", "alpha two", "alpha three")

	full := blocks(t, handleSearch, call, Args{"query": "alpha"})
	if len(full) != 2 {
		t.Fatalf("a complete search returned %d blocks: %q", len(full), full)
	}

	cut := blocks(t, handleSearch, call, Args{"query": "alpha", "limit": float64(2)})
	if len(cut) != 3 {
		t.Fatalf("a cut search returned %d blocks: %q", len(cut), cut)
	}
	if !strings.Contains(cut[2], "Stopped at 2 results") {
		t.Errorf("the third block is %q", cut[2])
	}
}

func TestReadMemory(t *testing.T) {
	call := project(t, "", "The body of the fact.")

	got := blocks(t, handleReadMemory, call, Args{"id": "fact-0"})
	if !strings.Contains(got[0], "The body of the fact.") || !strings.Contains(got[0], "---") {
		t.Errorf("the first block is not the raw file:\n%s", got[0])
	}
	if !strings.Contains(got[1], "\"id\": \"fact-0\"") {
		t.Errorf("the second block is not its fields:\n%s", got[1])
	}

	result := handleReadMemory(t.Context(), call, Args{"id": "no-such-thing"}).result()
	if !result.IsError || !strings.Contains(textOf(t, result)[0], "Search for it with mnemo_search_memories") {
		t.Errorf("a missing memory gave %q", textOf(t, result))
	}
}

func TestStatus(t *testing.T) {
	call := project(t, "## In progress\n\n- [ ] here\n- [ ] elsewhere [@other]\n\n## Done\n\n- [x] finished\n", "A fact.")

	got := blocks(t, handleStatus, call, Args{})
	if !strings.Contains(got[0], "machine: here") || !strings.Contains(got[0], "projects: 1 · memories: 1") {
		t.Errorf("the store block reads:\n%s", got[0])
	}
	if !strings.Contains(got[0], "autopush: off — confirm with the user") {
		t.Errorf("autopush is not explained:\n%s", got[0])
	}
	if strings.Contains(got[0], "project: shop") {
		t.Error("the store block carried a project nobody asked for")
	}

	withSlug := blocks(t, handleStatus, call, Args{"slug": "shop"})
	for _, want := range []string{
		"project: shop · active",
		"pending: 2 open — 2 in progress, 0 next",
		"1 belongs to another machine",
		"done: 1 ticked in the pending list — the card does not show them",
		"next: here",
	} {
		if !strings.Contains(withSlug[0], want) {
			t.Errorf("the project block does not say %q:\n%s", want, withSlug[0])
		}
	}
}

// The machine block is where an agent is told not to read the card as a record
// of what happened. Without it, work that was done gets proposed again.
func TestTheAgentIsToldWhatTheCardIsNotShowing(t *testing.T) {
	call := project(t, "## In progress\n\n- [ ] open one\n- [ ] not mine [@other]\n\n## Done\n\n- [x] finished\n")

	got := blocks(t, handleLoadProject, call, Args{"slug": "shop"})
	last := got[len(got)-1]
	for _, want := range []string{
		"This machine is 'here'. 1 pending item(s) belong to another machine",
		"1 item(s) are already done",
		"may have been done, not skipped",
		"**Machine-bound work.**",
	} {
		if !strings.Contains(last, want) {
			t.Errorf("the machine block does not say %q:\n%s", want, last)
		}
	}
}

// JSON goes to an agent, not to a web page. Escaping the three HTML characters
// would make a memory about a shell pipeline unreadable to whoever has to act
// on it.
func TestJSONIsReadable(t *testing.T) {
	got, err := encode(map[string]string{"shell": "a <b && c> d"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "a <b && c> d") {
		t.Errorf("the JSON escaped what it did not need to:\n%s", got)
	}
	if !strings.Contains(got, "\n  \"shell\"") {
		t.Errorf("the JSON is not indented by two spaces:\n%s", got)
	}
	if strings.HasSuffix(got, "\n") {
		t.Error("the block ends with a newline")
	}
}

func TestTheGuideSaysWhereToPasteIt(t *testing.T) {
	call := project(t, "")
	for target, want := range map[string]string{
		"agents":  "Paste the following into AGENTS.md:",
		"claude":  "Paste the following into CLAUDE.md:",
		"generic": "Paste the following into your agent's rules file:",
		"":        "Paste the following into your agent's rules file:",
	} {
		got := blocks(t, handleGuide, call, Args{"target": target})
		if got[0] != want {
			t.Errorf("target %q gave %q", target, got[0])
		}
		if !strings.Contains(got[1], "# mnemo — persistent project memory") {
			t.Errorf("target %q did not return the guide", target)
		}
	}
}

// Every tool is defined once, and the definition is what both the local server
// and the relay serve.
func TestTheDefinitionsAreUsable(t *testing.T) {
	names := map[string]bool{}
	for _, d := range memoryTools() {
		if d.Tool.Name == "" || d.Tool.Description == "" {
			t.Errorf("a tool has no name or no description: %+v", d.Tool)
		}
		if d.Handle == nil {
			t.Errorf("%s has no handler", d.Tool.Name)
		}
		if names[d.Tool.Name] {
			t.Errorf("%s is defined twice", d.Tool.Name)
		}
		names[d.Tool.Name] = true

		shape, ok := d.Tool.InputSchema.(schema)
		if !ok {
			t.Fatalf("%s has no input schema", d.Tool.Name)
		}
		if shape["type"] != "object" || shape["additionalProperties"] != false {
			t.Errorf("%s takes something other than a closed object", d.Tool.Name)
		}
		properties, _ := shape["properties"].(schema)
		for field, raw := range properties {
			if description, _ := raw.(schema)["description"].(string); description == "" {
				t.Errorf("%s.%s has no description; it is what an agent reads to decide", d.Tool.Name, field)
			}
		}
		for _, required := range shape["required"].([]string) {
			if _, ok := properties[required]; !ok {
				t.Errorf("%s requires %q, which is not one of its inputs", d.Tool.Name, required)
			}
		}
	}

	for _, want := range []string{
		"mnemo_status", "mnemo_list_projects", "mnemo_load_project",
		"mnemo_search_memories", "mnemo_read_memory", "mnemo_guide",
	} {
		if !names[want] {
			t.Errorf("%s is not offered", want)
		}
	}
}
