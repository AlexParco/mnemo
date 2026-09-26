package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/AlexParco/mnemo/internal/memory"
)

// Source is where a resolved value came from. `mnemo config` prints it, because
// "why is my store over there" is the question this package exists to answer.
type Source struct {
	// Kind is "flag", "env", "config" or "default".
	Kind string
	// Where names it: the flag, the variable or the config key. Empty for a default.
	Where string
}

func (s Source) String() string {
	if s.Where == "" {
		return s.Kind
	}
	return s.Kind + " " + s.Where
}

// Overrides are the values a command took from its own flags, which win over
// everything else.
type Overrides struct {
	Machine string
	Lang    string
	Store   string
}

// Settings is every setting, resolved, with the paths that follow from them.
type Settings struct {
	// Machine names this machine in addresses and in pending stamps. It is
	// normalised, and it can be empty when nothing usable was found.
	Machine string
	Lang    string
	Remote  string
	// Autopush is the user having said they do not want to be asked every time.
	Autopush bool
	Agent    string
	Tool     string
	Paths    Paths

	// Ignored lists settings whose value was not understood and was replaced by
	// the default. A value a person set and mnemo silently dropped is worse than
	// a refusal, so these are reported.
	Ignored []string

	source map[string]Source
}

// Resolve works out every setting: a flag beats the environment, which beats the
// file, which beats the default.
func Resolve(file *File, flags Overrides) Settings {
	if file == nil {
		file = &File{extra: map[string]any{}}
	}
	s := Settings{source: map[string]Source{}}

	// The store is resolved by Where, which has to agree with what is reported
	// here, so the reason is recorded separately rather than resolved twice.
	s.Paths = file.Where()
	if flags.Store != "" {
		s.Paths.Store = absolute(flags.Store)
		s.source["store"] = Source{"flag", "--store"}
	} else {
		s.source["store"] = pick(env("MNEMO_DIR") != "", Source{"env", "MNEMO_DIR"},
			file.Store.Dir != "", Source{"config", "store.dir"})
	}
	// The same condition StateDir uses: a relative XDG value is ignored, so
	// reporting it as the source would name a value that was not used.
	s.source["state"] = Source{"default", ""}
	if filepath.IsAbs(env("XDG_STATE_HOME")) {
		s.source["state"] = Source{"env", "XDG_STATE_HOME"}
	}
	s.source["mailbox"] = pick(env("MNEMO_MAILBOX_DIR") != "", Source{"env", "MNEMO_MAILBOX_DIR"},
		file.Mailbox.Dir != "", Source{"config", "mailbox.dir"})

	s.Machine, s.source["machine"] = resolveMachine(file, flags)
	s.Lang, s.source["lang"] = resolveLang(file, flags, &s)

	switch {
	case env("MNEMO_REMOTE") != "":
		s.Remote, s.source["remote"] = env("MNEMO_REMOTE"), Source{"env", "MNEMO_REMOTE"}
	case file.Store.Remote != "":
		s.Remote, s.source["remote"] = file.Store.Remote, Source{"config", "store.remote"}
	default:
		s.source["remote"] = Source{"default", ""}
	}

	switch raw := env("MNEMO_AUTOPUSH"); {
	case raw != "":
		s.Autopush, s.source["autopush"] = truthy(raw), Source{"env", "MNEMO_AUTOPUSH"}
	case file.Has("store.autopush"):
		s.Autopush, s.source["autopush"] = file.Store.Autopush, Source{"config", "store.autopush"}
	default:
		s.source["autopush"] = Source{"default", ""}
	}

	// An XDG variable that is not absolute is ignored, per the specification it
	// comes from. Saying so is the difference between a setting that did not
	// apply and a setting the user believes applied.
	for _, name := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME"} {
		if value := env(name); value != "" && !filepath.IsAbs(value) {
			s.Ignored = append(s.Ignored, fmt.Sprintf("env %s is %q, which is not an absolute path; using the default",
				name, value))
		}
	}
	// Without a home directory there is nowhere to default to, and a path that
	// came back empty must not be treated as the current directory.
	if s.Paths.Store == "" || s.Paths.State == "" {
		s.Ignored = append(s.Ignored, "this machine has no home directory, so mnemo cannot work out where to keep "+
			"the store; set MNEMO_DIR, or store.dir in the config file")
	}

	// The agent's name and the tool it runs in are never stored: they belong to
	// one chat, not to the machine.
	s.Agent, s.source["agent"] = env("MNEMO_AGENT"), Source{"env", "MNEMO_AGENT"}
	s.Tool, s.source["tool"] = env("MNEMO_TOOL"), Source{"env", "MNEMO_TOOL"}
	if s.Agent == "" {
		s.source["agent"] = Source{"default", ""}
	}
	if s.Tool == "" {
		s.source["tool"] = Source{"default", ""}
	}
	return s
}

// pick chooses the first source that applies, else the default.
func pick(firstApplies bool, first Source, secondApplies bool, second Source) Source {
	switch {
	case firstApplies:
		return first
	case secondApplies:
		return second
	default:
		return Source{"default", ""}
	}
}

// resolveMachine finds this machine's label and normalises it. The same
// normalisation is applied to the `[@machine]` stamps in a pending list, which
// is why it is the store's function and not a second copy of the rule: a label
// that normalises differently from a stamp would quietly stop matching it.
func resolveMachine(file *File, flags Overrides) (string, Source) {
	raw, source := "", Source{"default", ""}
	switch {
	case flags.Machine != "":
		raw, source = flags.Machine, Source{"flag", "--machine"}
	case env("MNEMO_MACHINE") != "":
		raw, source = env("MNEMO_MACHINE"), Source{"env", "MNEMO_MACHINE"}
	case file.Machine != "":
		raw, source = file.Machine, Source{"config", "machine"}
	default:
		host, _ := os.Hostname()
		// The hostname up to its first dot: a fully qualified name is the same
		// machine, and the label has to be short enough to read in an address.
		raw, _, _ = strings.Cut(host, ".")
		source = Source{"default", "hostname"}
	}
	return memory.NormalizeMachine(raw), source
}

// Languages mnemo renders the card in.
var languages = []string{"en", "es"}

func resolveLang(file *File, flags Overrides, s *Settings) (string, Source) {
	candidates := []struct {
		value  string
		source Source
	}{
		{flags.Lang, Source{"flag", "--lang"}},
		{env("MNEMO_LANG"), Source{"env", "MNEMO_LANG"}},
		{file.Lang, Source{"config", "lang"}},
	}
	for _, c := range candidates {
		if c.value == "" {
			continue
		}
		lang := strings.ToLower(strings.TrimSpace(c.value))
		for _, known := range languages {
			if lang == known {
				return lang, c.source
			}
		}
		// A language mnemo cannot render is reported, not obeyed and not hidden.
		s.Ignored = append(s.Ignored, fmt.Sprintf("%s is %q, which is not one of %s; using en",
			c.source, c.value, strings.Join(languages, ", ")))
		break
	}
	return "en", Source{"default", ""}
}

// truthy reads an on/off environment variable. Anything non-empty is on, except
// the three spellings of off, because `MNEMO_AUTOPUSH=0` has exactly one meaning
// to the person who typed it.
func truthy(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "0", "false", "no":
		return false
	default:
		return true
	}
}

// Source returns where one setting's value came from.
func (s Settings) Source(name string) Source { return s.source[name] }

// Named lists the settings `mnemo config` shows, in the order it shows them.
func (s Settings) Named() []string {
	out := make([]string, 0, len(s.source))
	for name := range s.source {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// RequireStore is for every command that touches memory. An empty path is not
// a store in the current directory: it is nowhere, and writing there would put
// the user's memory inside whatever repository the agent happened to be in —
// and, with a hub set, clone the whole of it in.
func (s Settings) RequireStore() (string, error) {
	if s.Paths.Store != "" {
		return s.Paths.Store, nil
	}
	return "", fmt.Errorf("there is nowhere to keep the store: this machine has no home directory that mnemo " +
		"could work one out from. Set MNEMO_DIR, or store.dir in the config file")
}

// RequireMachine is for the commands that cannot work without a label: the
// mailbox, and stamping a pending item. It says what to type rather than
// guessing a name the user would not recognise.
func (s Settings) RequireMachine() (string, error) {
	if s.Machine != "" {
		return s.Machine, nil
	}
	return "", fmt.Errorf("this machine has no usable label: its hostname gives nothing that can be used as one. " +
		"Set it with mnemo config set machine <label>")
}
