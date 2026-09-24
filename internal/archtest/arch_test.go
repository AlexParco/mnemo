// Package archtest enforces the dependency table in docs/architecture.md: which
// packages of this module may import which others.
//
// It only has tests. A package that is added to the module without being declared
// here fails the test, so the table cannot fall behind the code.
package archtest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strings"
	"testing"
)

const module = "github.com/AlexParco/mnemo"

// External modules, matched on whole path segments so that their subpackages
// (for example go-sdk/mcp) are covered.
const (
	sdk   = "github.com/modelcontextprotocol/go-sdk"
	flock = "github.com/gofrs/flock"
	toml  = "github.com/BurntSushi/toml"
	sjson = "github.com/tidwall/sjson"
	gjson = "github.com/tidwall/gjson"
)

// anything allows every import. Only the command layer and the test helpers have it.
const anything = "*"

// allowed mirrors the table in docs/architecture.md. Keys and internal entries are
// paths relative to the module, matched exactly: a subpackage is declared on its own.
// The standard library is always allowed and never listed.
var allowed = map[string][]string{
	"cmd/mnemo":            {"internal/cli"},
	"internal/cli":         {anything},
	"internal/config":      {toml},
	"internal/lock":        {flock},
	"internal/memory":      {},
	"internal/gitx":        {"internal/lock"},
	"internal/store":       {"internal/memory", "internal/gitx", "internal/lock", "templates"},
	"internal/mailbox":     {"internal/lock"},
	"internal/criterion":   {},
	"internal/mcpserver":   {"internal/store", "internal/gitx", "internal/mailbox", "internal/memory", "internal/criterion", "internal/config", sdk},
	"internal/httpapi":     {"internal/mcpserver", "internal/mailbox", "internal/config", sdk},
	"internal/remote":      {"internal/mcpserver", "internal/mailbox", "internal/config", "internal/tunnel", sdk},
	"internal/tunnel":      {"internal/config", "internal/lock"},
	"internal/service":     {"internal/config"},
	"internal/integration": {"internal/config", "internal/mailbox", "internal/remote", sjson, gjson, toml},
	"internal/hook":        {"internal/config"},
	"internal/selfupdate":  {"internal/config", "internal/service", "internal/tunnel"},
	"internal/testutil":    {anything},
	"internal/archtest":    {},
	"templates":            {},
}

type goPackage struct {
	ImportPath string
	Imports    []string
}

// violations returns, sorted, one line per package the table does not declare and
// one per import the table does not allow. Only non-test imports are checked: tests
// may import helpers such as internal/testutil.
func violations(table map[string][]string, pkgs []goPackage) []string {
	var out []string
	for _, p := range pkgs {
		rel, ours := relative(p.ImportPath)
		if !ours {
			continue
		}
		rules, declared := table[rel]
		if !declared {
			out = append(out, fmt.Sprintf("%s is not in the dependency table: add it to docs/architecture.md and to allowed", rel))
			continue
		}
		for _, imp := range p.Imports {
			if !permits(rules, imp) {
				out = append(out, fmt.Sprintf("%s imports %s, which the dependency table does not allow", rel, imp))
			}
		}
	}
	sort.Strings(out)
	return out
}

// relative turns a package path of this module into its path inside the module.
func relative(path string) (string, bool) {
	if path == module {
		return ".", true
	}
	if rest, ok := strings.CutPrefix(path, module+"/"); ok {
		return rest, true
	}
	return "", false
}

func permits(rules []string, imp string) bool {
	if isStandard(imp) {
		return true
	}
	rel, internal := relative(imp)
	for _, rule := range rules {
		switch {
		case rule == anything:
			return true
		case internal:
			if rel == rule {
				return true
			}
		case imp == rule || strings.HasPrefix(imp, rule+"/"):
			return true
		}
	}
	return false
}

// isStandard reports whether a path belongs to the standard library, whose first
// path element never contains a dot.
func isStandard(path string) bool {
	first, _, _ := strings.Cut(path, "/")
	return !strings.Contains(first, ".")
}

func TestViolations(t *testing.T) {
	table := map[string][]string{
		"internal/memory": {},
		"internal/store":  {"internal/memory"},
		"internal/lock":   {flock},
		"internal/cli":    {anything},
	}
	pkg := func(rel string, imports ...string) goPackage {
		return goPackage{ImportPath: module + "/" + rel, Imports: imports}
	}

	cases := []struct {
		name string
		pkgs []goPackage
		want []string
	}{
		{
			name: "the standard library is always allowed",
			pkgs: []goPackage{pkg("internal/memory", "fmt", "encoding/json", "C")},
		},
		{
			name: "a listed internal import is allowed",
			pkgs: []goPackage{pkg("internal/store", module+"/internal/memory")},
		},
		{
			name: "an internal import that is not listed is refused",
			pkgs: []goPackage{pkg("internal/memory", module+"/internal/store")},
			want: []string{"internal/memory imports " + module + "/internal/store, which the dependency table does not allow"},
		},
		{
			name: "a listed internal package does not cover its subpackages",
			pkgs: []goPackage{pkg("internal/store", module+"/internal/memory/sub")},
			want: []string{"internal/store imports " + module + "/internal/memory/sub, which the dependency table does not allow"},
		},
		{
			name: "an external module covers its subpackages",
			pkgs: []goPackage{pkg("internal/lock", flock, flock+"/internal")},
		},
		{
			name: "an external prefix only matches whole segments",
			pkgs: []goPackage{pkg("internal/lock", flock+"x")},
			want: []string{"internal/lock imports " + flock + "x, which the dependency table does not allow"},
		},
		{
			name: "an unlisted external module is refused",
			pkgs: []goPackage{pkg("internal/memory", sjson)},
			want: []string{"internal/memory imports " + sjson + ", which the dependency table does not allow"},
		},
		{
			name: "anything allows every import",
			pkgs: []goPackage{pkg("internal/cli", module+"/internal/store", sdk+"/mcp")},
		},
		{
			name: "a package missing from the table is reported",
			pkgs: []goPackage{pkg("internal/undeclared", "fmt")},
			want: []string{"internal/undeclared is not in the dependency table: add it to docs/architecture.md and to allowed"},
		},
		{
			name: "the module root is a package like any other",
			pkgs: []goPackage{{ImportPath: module}},
			want: []string{". is not in the dependency table: add it to docs/architecture.md and to allowed"},
		},
		{
			name: "packages of other modules are ignored",
			pkgs: []goPackage{{ImportPath: "example.com/other", Imports: []string{module + "/internal/store"}}},
		},
		{
			name: "several findings come back sorted",
			pkgs: []goPackage{
				pkg("internal/store", sjson),
				pkg("internal/memory", module+"/internal/store"),
			},
			want: []string{
				"internal/memory imports " + module + "/internal/store, which the dependency table does not allow",
				"internal/store imports " + sjson + ", which the dependency table does not allow",
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := violations(table, c.pkgs)
			if strings.Join(got, "\n") != strings.Join(c.want, "\n") {
				t.Errorf("violations:\n got: %q\nwant: %q", got, c.want)
			}
		})
	}
}

// TestModuleImports checks the real module against the table.
func TestModuleImports(t *testing.T) {
	cmd := exec.Command("go", "list", "-json", "./...")
	cmd.Dir = moduleRoot(t)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list failed: %v\n%s", err, stderr.String())
	}

	var pkgs []goPackage
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var p goPackage
		if err := dec.Decode(&p); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatalf("decoding go list output: %v", err)
		}
		pkgs = append(pkgs, p)
	}

	// Guard against a vacuous pass: this package must be among those listed, or go
	// list was not looking at the module at all.
	seen := false
	for _, p := range pkgs {
		if p.ImportPath == module+"/internal/archtest" {
			seen = true
		}
	}
	if !seen {
		t.Fatalf("go list did not report %s/internal/archtest; it is not listing this module", module)
	}

	for _, v := range violations(allowed, pkgs) {
		t.Error(v)
	}
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		t.Fatalf("go env GOMOD: %v", err)
	}
	gomod := strings.TrimSpace(string(out))
	if gomod == "" || gomod == "/dev/null" || gomod == "NUL" {
		t.Fatal("not inside a Go module")
	}
	return strings.TrimSuffix(gomod, "/go.mod")
}
