package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isolate cuts the test off from the machine it runs on. Without it the
// developer's own XDG variables and home directory decide where the store is,
// and the defaults would never be exercised.
func isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for _, name := range []string{
		"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME",
		"MNEMO_CONFIG", "MNEMO_DIR", "MNEMO_MAILBOX_DIR", "MNEMO_REMOTE",
		"MNEMO_MACHINE", "MNEMO_LANG", "MNEMO_AUTOPUSH", "MNEMO_AGENT", "MNEMO_TOOL",
	} {
		t.Setenv(name, "")
	}
	return home
}

// write puts a config file where MNEMO_CONFIG points and loads it.
func write(t *testing.T, body string) *File {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MNEMO_CONFIG", path)
	file, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("loading: %v", err)
	}
	return file
}

func TestTheDefaultsWithNothingSet(t *testing.T) {
	home := isolate(t)
	file, err := LoadFrom(ConfigFile())
	if err != nil {
		t.Fatal(err)
	}
	got := Resolve(file, Overrides{})

	want := map[string]string{
		"Config":   filepath.Join(home, ".config", "mnemo", "config.toml"),
		"Store":    filepath.Join(home, ".local", "share", "mnemo"),
		"State":    filepath.Join(home, ".local", "state", "mnemo"),
		"Locks":    filepath.Join(home, ".local", "state", "mnemo", "locks"),
		"Mailbox":  filepath.Join(home, ".local", "state", "mnemo", "mailbox"),
		"Tunnel":   filepath.Join(home, ".local", "state", "mnemo", "tunnel"),
		"Logs":     filepath.Join(home, ".local", "state", "mnemo", "logs"),
		"Reminder": filepath.Join(home, ".local", "state", "mnemo", "suggest-save"),
	}
	have := map[string]string{
		"Config": got.Paths.Config, "Store": got.Paths.Store, "State": got.Paths.State,
		"Locks": got.Paths.Locks, "Mailbox": got.Paths.Mailbox, "Tunnel": got.Paths.Tunnel,
		"Logs": got.Paths.Logs, "Reminder": got.Paths.Reminder,
	}
	for name, path := range want {
		if have[name] != path {
			t.Errorf("%s is %s, want %s", name, have[name], path)
		}
	}

	// The store is not created by asking where it is.
	if _, err := os.Stat(got.Paths.Store); !os.IsNotExist(err) {
		t.Error("resolving a path created it")
	}
	if got.Lang != "en" || got.Remote != "" || got.Autopush {
		t.Errorf("settings = %+v, want the defaults", got)
	}
}

func TestXDGMovesEverything(t *testing.T) {
	isolate(t)
	base := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(base, "cfg"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(base, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(base, "state"))

	got := Resolve(&File{}, Overrides{})
	if got.Paths.Config != filepath.Join(base, "cfg", "mnemo", "config.toml") {
		t.Errorf("config is at %s", got.Paths.Config)
	}
	if got.Paths.Store != filepath.Join(base, "data", "mnemo") {
		t.Errorf("store is at %s", got.Paths.Store)
	}
	if got.Paths.Locks != filepath.Join(base, "state", "mnemo", "locks") {
		t.Errorf("locks are at %s", got.Paths.Locks)
	}
}

// A relative XDG value is ignored by the specification, and by us: it would put
// the store wherever the shell happened to be.
func TestARelativeXDGValueIsIgnored(t *testing.T) {
	home := isolate(t)
	t.Setenv("XDG_DATA_HOME", "relative/data")
	if got := Resolve(&File{}, Overrides{}); got.Paths.Store != filepath.Join(home, ".local", "share", "mnemo") {
		t.Errorf("store is at %s, want the default", got.Paths.Store)
	}
}

func TestWhatWinsOverWhat(t *testing.T) {
	isolate(t)
	file := write(t, "machine = \"from-file\"\nlang = \"es\"\n\n[store]\ndir = \"/from/file\"\nremote = \"file:remote\"\nautopush = true\n")

	t.Run("the file, when nothing else is set", func(t *testing.T) {
		got := Resolve(file, Overrides{})
		if got.Machine != "from-file" || got.Lang != "es" || got.Remote != "file:remote" || !got.Autopush {
			t.Errorf("settings = %+v", got)
		}
		if got.Paths.Store != filepath.FromSlash("/from/file") {
			t.Errorf("store is %s", got.Paths.Store)
		}
		if s := got.Source("machine"); s.Kind != "config" || s.Where != "machine" {
			t.Errorf("machine came from %s", s)
		}
	})

	t.Run("the environment beats the file", func(t *testing.T) {
		t.Setenv("MNEMO_MACHINE", "from-env")
		t.Setenv("MNEMO_LANG", "en")
		t.Setenv("MNEMO_DIR", filepath.FromSlash("/from/env"))
		t.Setenv("MNEMO_REMOTE", "env:remote")
		t.Setenv("MNEMO_AUTOPUSH", "0")

		got := Resolve(file, Overrides{})
		if got.Machine != "from-env" || got.Lang != "en" || got.Remote != "env:remote" || got.Autopush {
			t.Errorf("settings = %+v", got)
		}
		if got.Paths.Store != filepath.FromSlash("/from/env") {
			t.Errorf("store is %s", got.Paths.Store)
		}
		if s := got.Source("autopush"); s.String() != "env MNEMO_AUTOPUSH" {
			t.Errorf("autopush came from %s", s)
		}
	})

	t.Run("a flag beats the environment", func(t *testing.T) {
		t.Setenv("MNEMO_MACHINE", "from-env")
		t.Setenv("MNEMO_DIR", filepath.FromSlash("/from/env"))

		got := Resolve(file, Overrides{Machine: "from-flag", Store: filepath.FromSlash("/from/flag"), Lang: "es"})
		if got.Machine != "from-flag" || got.Lang != "es" {
			t.Errorf("settings = %+v", got)
		}
		if got.Paths.Store != filepath.FromSlash("/from/flag") {
			t.Errorf("store is %s", got.Paths.Store)
		}
		if s := got.Source("store"); s.String() != "flag --store" {
			t.Errorf("the store came from %s", s)
		}
	})
}

// A wrapper script that exports a variable it did not fill in must not push the
// store somewhere unexpected.
func TestAnEmptyVariableCountsAsUnset(t *testing.T) {
	isolate(t)
	file := write(t, "machine = \"from-file\"\n\n[store]\ndir = \"/from/file\"\n")
	t.Setenv("MNEMO_DIR", "")
	t.Setenv("MNEMO_MACHINE", "   ")

	got := Resolve(file, Overrides{})
	if got.Paths.Store != filepath.FromSlash("/from/file") {
		t.Errorf("store is %s, want the file's", got.Paths.Store)
	}
	if got.Machine != "from-file" {
		t.Errorf("machine is %q, want the file's", got.Machine)
	}
}

// Resolved once, against the directory the process was in: a server that walks
// into a repository must not take its store with it.
func TestARelativeStoreIsPinnedToTheWorkingDirectory(t *testing.T) {
	isolate(t)
	t.Setenv("MNEMO_DIR", "sideways")
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	got := Resolve(&File{}, Overrides{})
	if got.Paths.Store != filepath.Join(cwd, "sideways") {
		t.Errorf("store is %s, want it under %s", got.Paths.Store, cwd)
	}
	if !filepath.IsAbs(got.Paths.Store) {
		t.Error("the store path is not absolute")
	}
}

func TestAutopushReadsWhatAPersonMeant(t *testing.T) {
	isolate(t)
	for raw, want := range map[string]bool{
		"0": false, "false": false, "no": false, "FALSE": false, "No": false,
		"1": true, "true": true, "yes": true, "on": true, "anything": true,
	} {
		t.Setenv("MNEMO_AUTOPUSH", raw)
		if got := Resolve(&File{}, Overrides{}); got.Autopush != want {
			t.Errorf("MNEMO_AUTOPUSH=%q gave %v, want %v", raw, got.Autopush, want)
		}
	}
}

// `autopush = false` in the file is a decision; no key at all is a question
// never asked. They must not look the same when `mnemo config` explains itself.
func TestOffInTheFileIsNotTheSameAsAbsent(t *testing.T) {
	isolate(t)
	written := Resolve(write(t, "[store]\nautopush = false\n"), Overrides{})
	if written.Autopush {
		t.Error("autopush is on")
	}
	if s := written.Source("autopush"); s.String() != "config store.autopush" {
		t.Errorf("a written false came from %s", s)
	}

	absent := Resolve(write(t, "[store]\nremote = \"somewhere\"\n"), Overrides{})
	if s := absent.Source("autopush"); s.String() != "default" {
		t.Errorf("an absent key came from %s", s)
	}
}

func TestTheMachineLabel(t *testing.T) {
	isolate(t)
	for raw, want := range map[string]string{
		"Laptop1":         "laptop1",
		"MacBook Pro":     "macbook-pro",
		"Ñandú":           "nandu",
		"  spaced  ":      "spaced",
		"--edges--":       "edges",
		"a_very.odd@name": "a-very-odd-name",
	} {
		t.Setenv("MNEMO_MACHINE", raw)
		if got := Resolve(&File{}, Overrides{}); got.Machine != want {
			t.Errorf("%q gave %q, want %q", raw, got.Machine, want)
		}
	}

	t.Run("a label that normalises to nothing", func(t *testing.T) {
		t.Setenv("MNEMO_MACHINE", "!!!")
		got := Resolve(&File{}, Overrides{})
		if got.Machine != "" {
			t.Fatalf("machine is %q", got.Machine)
		}
		_, err := got.RequireMachine()
		if err == nil {
			t.Fatal("a machine with no label was accepted")
		}
		if !strings.Contains(err.Error(), "mnemo config set machine") {
			t.Errorf("the message does not say what to type: %v", err)
		}
	})
}

func TestALanguageMnemoCannotRenderIsReported(t *testing.T) {
	isolate(t)
	t.Setenv("MNEMO_LANG", "fr")

	got := Resolve(&File{}, Overrides{})
	if got.Lang != "en" {
		t.Errorf("lang is %q, want the default", got.Lang)
	}
	if len(got.Ignored) != 1 || !strings.Contains(got.Ignored[0], "MNEMO_LANG") {
		// Fatal, not an error: the next line indexes this slice, and a panic
		// would bury the assertion that actually failed.
		t.Fatalf("ignored = %q, want one line naming the variable", got.Ignored)
	}
	if !strings.Contains(got.Ignored[0], "\"fr\"") {
		t.Errorf("the report does not say what was ignored: %q", got.Ignored[0])
	}
}

func TestAMissingFileIsNotAnError(t *testing.T) {
	isolate(t)
	file, err := LoadFrom(filepath.Join(t.TempDir(), "nothing.toml"))
	if err != nil {
		t.Fatalf("a missing config was an error: %v", err)
	}
	if got := Resolve(file, Overrides{}); got.Lang != "en" {
		t.Errorf("settings = %+v, want the defaults", got)
	}
}

func TestBrokenTOMLNamesTheFile(t *testing.T) {
	isolate(t)
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("machine = \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadFrom(path)
	if err == nil {
		t.Fatal("broken TOML was accepted")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("the message does not name the file: %v", err)
	}
}

// The user's own keys, and keys from a newer mnemo, survive a rewrite. Losing
// them would be mnemo deleting something a person typed.
func TestARewriteKeepsWhatItDoesNotUnderstand(t *testing.T) {
	isolate(t)
	path := filepath.Join(t.TempDir(), "config.toml")
	original := "" +
		"machine = \"old\"\n" +
		"something_new = 42\n" +
		"\n" +
		"[store]\n" +
		"remote = \"keep:me\"\n" +
		"future_key = \"from a newer mnemo\"\n" +
		"\n" +
		"[experiment]\n" +
		"enabled = true\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	file, err := LoadFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := file.Unknown(); strings.Join(got, ",") != "experiment.enabled,something_new,store.future_key" {
		t.Errorf("unknown = %q", got)
	}

	file.Machine = "new"
	file.Store.Autopush = true
	if err := file.Save(); err != nil {
		t.Fatal(err)
	}

	again, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("what we wrote does not load: %v", err)
	}
	if again.Machine != "new" || !again.Store.Autopush || again.Store.Remote != "keep:me" {
		t.Errorf("settings after the rewrite: %+v", again)
	}
	if got := again.Unknown(); strings.Join(got, ",") != "experiment.enabled,something_new,store.future_key" {
		t.Errorf("the rewrite lost keys: %q", got)
	}
}

// The file can hold the server's token.
func TestTheFileIsWrittenPrivatelyAndWithoutLitter(t *testing.T) {
	isolate(t)
	dir := filepath.Join(t.TempDir(), "fresh")
	path := filepath.Join(dir, "mnemo", "config.toml")

	file, err := LoadFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	file.Server.Token = "not-a-real-token"
	if err := file.Save(); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("the file is %o, want 600", mode)
	}
	// The token is the reason for the mode. A file written 0600 with the token
	// missing would pass every other assertion here and lose the thing it was
	// protecting.
	if body := read(t, path); !strings.Contains(body, "not-a-real-token") {
		t.Errorf("the token was not written:\n%s", body)
	}
	parent, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if mode := parent.Mode().Perm(); mode != 0o700 {
		t.Errorf("the directory is %o, want 700", mode)
	}

	// The temporary file is gone: a rename replaced the real one.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "config.toml" {
			t.Errorf("%s was left behind", e.Name())
		}
	}
}

func TestSavingSomethingThatCameFromNowhere(t *testing.T) {
	isolate(t)
	if err := (&File{}).Save(); err == nil {
		t.Error("a config with no path was saved somewhere")
	}
}

// The agent's name and the tool belong to one chat and are never stored.
func TestTheAgentAndToolComeOnlyFromTheEnvironment(t *testing.T) {
	isolate(t)
	t.Setenv("MNEMO_AGENT", "worker")
	t.Setenv("MNEMO_TOOL", "claude")

	got := Resolve(write(t, "machine = \"here\"\n"), Overrides{})
	if got.Agent != "worker" || got.Tool != "claude" {
		t.Errorf("agent %q, tool %q", got.Agent, got.Tool)
	}
	if s := got.Source("agent"); s.String() != "env MNEMO_AGENT" {
		t.Errorf("the agent came from %s", s)
	}

	file := write(t, "machine = \"here\"\n")
	file.Machine = "here"
	if err := file.Save(); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(file.Path())
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"worker", "claude", "agent", "tool"} {
		if strings.Contains(string(body), leak) {
			t.Errorf("the config file mentions %q:\n%s", leak, body)
		}
	}
}

// A person writing ~/memories means their home. Without expansion the value is
// merely not absolute, so it would become a directory called `~` next to
// wherever the shell happened to be, and their memory would be somewhere they
// would never look for it.
func TestATildeMeansHome(t *testing.T) {
	home := isolate(t)

	t.Run("from the environment", func(t *testing.T) {
		t.Setenv("MNEMO_DIR", "~/memories")
		if got := Resolve(&File{}, Overrides{}).Paths.Store; got != filepath.Join(home, "memories") {
			t.Errorf("store is %s, want it under the home directory", got)
		}
	})

	t.Run("from the config file", func(t *testing.T) {
		got := Resolve(write(t, "[store]\ndir = \"~\"\n"), Overrides{})
		if got.Paths.Store != home {
			t.Errorf("store is %s, want %s", got.Paths.Store, home)
		}
	})

	t.Run("another account's home is not guessed at", func(t *testing.T) {
		t.Setenv("MNEMO_DIR", "~someone/memories")
		cwd, _ := os.Getwd()
		if got := Resolve(&File{}, Overrides{}).Paths.Store; got != filepath.Join(cwd, "~someone", "memories") {
			t.Errorf("store is %s; ~user is left alone deliberately", got)
		}
	})
}

// A remote written by a newer mnemo carries fields this one does not know. They
// are exactly the kind of thing that must survive a rewrite, since the list
// exists so a second server can be added without a migration.
func TestARewriteKeepsFieldsInsideARemote(t *testing.T) {
	isolate(t)
	path := filepath.Join(t.TempDir(), "config.toml")
	original := "" +
		"[[remotes]]\n" +
		"machine = \"hub\"\n" +
		"ssh = \"user@hub\"\n" +
		"server_port = 7433\n" +
		"local_port = 7433\n" +
		"token = \"kept\"\n" +
		"connected = true\n" +
		"future_field = \"from a newer mnemo\"\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	file, err := LoadFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(file.Remotes) != 1 || file.Remotes[0].Machine != "hub" || file.Remotes[0].Token != "kept" {
		t.Fatalf("remotes = %+v", file.Remotes)
	}

	file.Remotes[0].Connected = false
	if err := file.Save(); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "future_field") {
		t.Errorf("a field inside the remote was dropped:\n%s", body)
	}

	again, err := LoadFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Remotes) != 1 || again.Remotes[0].Connected || again.Remotes[0].Token != "kept" {
		t.Errorf("remotes after the rewrite: %+v", again.Remotes)
	}
}

// Saving must not turn mnemo's own settings into keys it does not recognise.
func TestSavingDoesNotConfuseItsOwnKeysForUnknownOnes(t *testing.T) {
	isolate(t)
	file := write(t, "[store]\nflavor = \"vanilla\"\ndir = \"/s\"\n")
	before := strings.Join(file.Unknown(), ",")
	if err := file.Save(); err != nil {
		t.Fatal(err)
	}
	if after := strings.Join(file.Unknown(), ","); after != before {
		t.Errorf("unknown keys went from %q to %q", before, after)
	}
}

// Off has to be a decision the user made, not something every unrelated save
// writes in on their behalf.
func TestAutopushIsOnlyWrittenWhenItWasChosen(t *testing.T) {
	isolate(t)

	t.Run("never chosen, never written", func(t *testing.T) {
		file := write(t, "machine = \"here\"\n")
		file.Machine = "there"
		if err := file.Save(); err != nil {
			t.Fatal(err)
		}
		if body := read(t, file.Path()); strings.Contains(body, "autopush") {
			t.Errorf("an unrelated save invented a decision:\n%s", body)
		}
		again, err := LoadFrom(file.Path())
		if err != nil {
			t.Fatal(err)
		}
		if Resolve(again, Overrides{}).Source("autopush").String() != "default" {
			t.Error("the rewrite turned a default into a setting")
		}
	})

	t.Run("chosen off, kept off", func(t *testing.T) {
		file := write(t, "machine = \"here\"\n")
		file.SetAutopush(false)
		if err := file.Save(); err != nil {
			t.Fatal(err)
		}
		if body := read(t, file.Path()); !strings.Contains(body, "autopush = false") {
			t.Errorf("a deliberate off was not written:\n%s", body)
		}
	})

	t.Run("already in the file, kept", func(t *testing.T) {
		file := write(t, "[store]\nautopush = false\n")
		if err := file.Save(); err != nil {
			t.Fatal(err)
		}
		if body := read(t, file.Path()); !strings.Contains(body, "autopush = false") {
			t.Errorf("the rewrite dropped it:\n%s", body)
		}
	})
}

// A setting that did not apply is reported. Silence would leave the user
// believing their variable took effect.
func TestSettingsThatDidNotApplyAreReported(t *testing.T) {
	home := isolate(t)
	t.Setenv("XDG_STATE_HOME", "relative/state")

	got := Resolve(&File{}, Overrides{})
	if got.Paths.State != filepath.Join(home, ".local", "state", "mnemo") {
		t.Errorf("state is %s, want the default", got.Paths.State)
	}
	if s := got.Source("state"); s.String() != "default" {
		t.Errorf("the state directory claims to come from %s, but the value was ignored", s)
	}
	if len(got.Ignored) != 1 || !strings.Contains(got.Ignored[0], "XDG_STATE_HOME") {
		t.Fatalf("ignored = %q, want one line naming the variable", got.Ignored)
	}
}

func TestTheHostnameIsNamedAsTheSource(t *testing.T) {
	isolate(t)
	if s := Resolve(&File{}, Overrides{}).Source("machine"); s.String() != "default hostname" {
		t.Errorf("the machine label came from %s, want the hostname named", s)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(body)
}

// Replacing the file, proved rather than assumed.
//
// A hard link is a second name for the same bytes. If Save renames a temporary
// file over the destination, the destination becomes a different file and the
// witness still holds the old contents. If Save writes in place, both names see
// the new contents. Nothing else distinguishes the two afterwards, which is why
// the earlier test — which only checks that no temporary file was left — cannot
// tell them apart and does not claim to.
func TestSavingReplacesTheFileByRenaming(t *testing.T) {
	isolate(t)
	file := write(t, "machine = \"before\"\n")
	witness := file.Path() + ".witness"
	if err := os.Link(file.Path(), witness); err != nil {
		t.Skipf("this filesystem has no hard links: %v", err)
	}

	file.Machine = "after"
	if err := file.Save(); err != nil {
		t.Fatal(err)
	}

	if body := read(t, file.Path()); !strings.Contains(body, "after") {
		t.Fatalf("the save did not take:\n%s", body)
	}
	if body := read(t, witness); strings.Contains(body, "after") {
		t.Error("the file was written in place, so a reader can catch it half-written")
	} else if !strings.Contains(body, "before") {
		t.Errorf("the witness holds neither version:\n%s", body)
	}
}

// A value that reaches the file unvalidated is read by every later command as
// if it were meant: `lang = "fr"` becomes a warning on every connection for
// ever, and nobody remembers typing it.
func TestSettingIsValidatedBeforeItIsWritten(t *testing.T) {
	isolate(t)
	file := write(t, "")

	t.Run("a language mnemo cannot render", func(t *testing.T) {
		if err := file.Set("lang", "fr"); err == nil {
			t.Fatal("fr was accepted")
		} else if !strings.Contains(err.Error(), "en, es") {
			t.Errorf("the message does not say what is allowed: %v", err)
		}
	})

	t.Run("autopush is a yes or a no", func(t *testing.T) {
		if err := file.Set("store.autopush", "maybe"); err == nil {
			t.Fatal("maybe was accepted")
		}
		if err := file.Set("store.autopush", "false"); err != nil {
			t.Fatal(err)
		}
		// Written, because off chosen is a decision, and reported as one.
		if err := file.Save(); err != nil {
			t.Fatal(err)
		}
		if body := read(t, file.Path()); !strings.Contains(body, "autopush = false") {
			t.Errorf("a deliberate off was not written:\n%s", body)
		}
	})

	t.Run("a machine label is stored normalised", func(t *testing.T) {
		if err := file.Set("machine", "My Laptop"); err != nil {
			t.Fatal(err)
		}
		if file.Machine != "my-laptop" {
			t.Errorf("machine is %q, want it normalised so every command reads the same label", file.Machine)
		}
		if err := file.Set("machine", "!!!"); err == nil {
			t.Error("a label that normalises to nothing was accepted")
		}
	})

	t.Run("the sections other commands own", func(t *testing.T) {
		for key, owner := range map[string]string{
			"server.token":    "mnemo server setup",
			"server.port":     "mnemo server setup",
			"remotes":         "mnemo connect",
			"remotes.machine": "mnemo connect",
		} {
			err := file.Set(key, "x")
			if err == nil {
				t.Errorf("%s was accepted", key)
				continue
			}
			if !strings.Contains(err.Error(), owner) {
				t.Errorf("setting %s does not name %s: %v", key, owner, err)
			}
		}
	})

	t.Run("a key that is not a setting names the ones that are", func(t *testing.T) {
		err := file.Set("stroe.dir", "/somewhere")
		if err == nil {
			t.Fatal("a misspelt key was accepted")
		}
		for _, want := range []string{"store.dir", "machine", "lang"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the message does not list %s: %v", want, err)
			}
		}
	})
}

// Unsetting really removes the key, so the default applies again rather than
// the value being written back as false.
func TestUnsettingBringsTheDefaultBack(t *testing.T) {
	isolate(t)
	file := write(t, "lang = \"es\"\n\n[store]\nautopush = true\nremote = \"somewhere\"\n")

	for _, key := range []string{"lang", "store.autopush", "store.remote"} {
		if err := file.Unset(key); err != nil {
			t.Fatalf("unsetting %s: %v", key, err)
		}
	}
	if err := file.Save(); err != nil {
		t.Fatal(err)
	}

	body := read(t, file.Path())
	for _, gone := range []string{"lang", "autopush", "remote"} {
		if strings.Contains(body, gone) {
			t.Errorf("%s survived the unset:\n%s", gone, body)
		}
	}
	again, err := LoadFrom(file.Path())
	if err != nil {
		t.Fatal(err)
	}
	resolved := Resolve(again, Overrides{})
	if resolved.Lang != "en" || resolved.Autopush || resolved.Remote != "" {
		t.Errorf("settings = %+v, want the defaults back", resolved)
	}
	if s := resolved.Source("autopush"); s.String() != "default" {
		t.Errorf("autopush still claims to come from %s", s)
	}

	if err := file.Unset("server.token"); err == nil {
		t.Error("a managed key was unset")
	}
}

// A config listing goes into terminals, transcripts and screenshots.
func TestATokenIsNeverItsOwnValue(t *testing.T) {
	if got := Mask("a-real-looking-token"); got == "a-real-looking-token" || got == "" {
		t.Errorf("a token masks to %q", got)
	}
	if got := Mask(""); got != "" {
		t.Errorf("nothing masks to %q, want nothing", got)
	}
}
