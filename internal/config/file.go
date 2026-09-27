package config

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/AlexParco/mnemo/internal/memory"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// File is the config file's contents. Every field is optional: a machine with no
// config file at all works, on defaults.
type File struct {
	Machine string   `toml:"machine"`
	Lang    string   `toml:"lang"`
	Store   Store    `toml:"store"`
	Mailbox Mailbox  `toml:"mailbox"`
	Server  Server   `toml:"server"`
	Remotes []Remote `toml:"remotes"`

	// path is where this was read from, so Save writes it back to the same place.
	path string
	// defined tells a key that was written as false from a key that was never
	// written at all. For `autopush = false` the difference matters: one is a
	// decision the user made, the other is a question they never answered.
	defined toml.MetaData
	// rawRemotes keeps each remote as it was written, so a field a newer mnemo
	// added to an entry survives this one rewriting the file. The struct above
	// carries the fields we understand; these carry the rest.
	rawRemotes []map[string]any
	// touched marks a key a caller set deliberately, which is how `autopush =
	// false` written on purpose is told from a key that was never there.
	touched map[string]bool
	// undefined are keys an unset removed, so Has stops reporting them even
	// though the parsed file still carries them.
	undefined []string
	// extra holds the keys mnemo does not know about. A person may edit this file
	// by hand, and a setting from a newer version, or a note to themselves, must
	// survive mnemo rewriting the file. Known keys are stripped out at load so
	// they cannot go stale.
	extra map[string]any
}

// Store is the memory store's settings.
type Store struct {
	Dir      string `toml:"dir"`
	Remote   string `toml:"remote"`
	Autopush bool   `toml:"autopush"`
}

// Mailbox is where messages between agents are kept.
type Mailbox struct {
	Dir string `toml:"dir"`
}

// Server is written by `mnemo server setup`, on the machine that hosts.
type Server struct {
	Port  int    `toml:"port"`
	Token string `toml:"token"`
}

// Remote is written by `mnemo connect`, on a machine that uses someone else's
// server. It is a list so a second one can be added without a migration.
type Remote struct {
	Machine    string `toml:"machine"`
	SSH        string `toml:"ssh"`
	ServerPort int    `toml:"server_port"`
	LocalPort  int    `toml:"local_port"`
	Token      string `toml:"token"`
	Connected  bool   `toml:"connected"`
}

// known lists the keys mnemo owns, in dotted form. Anything else in the file is
// somebody else's and is kept untouched.
var known = []string{
	"machine", "lang",
	"store.dir", "store.remote", "store.autopush",
	"mailbox.dir",
	"server.port", "server.token",
	"remotes",
}

// Load reads the config file. A file that is not there is not an error: it means
// every setting is on its default, which is the normal state of a fresh machine.
func Load() (*File, error) {
	return LoadFrom(ConfigFile())
}

// LoadFrom reads a named file, for tests and for `mnemo config --file`.
func LoadFrom(path string) (*File, error) {
	file := &File{path: path, extra: map[string]any{}, touched: map[string]bool{}}
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return file, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	meta, err := toml.Decode(string(raw), file)
	if err != nil {
		// The message names the file, because the user edited it by hand and the
		// line number is the only useful thing we can give them.
		return nil, fmt.Errorf("%s is not valid TOML: %w", path, err)
	}
	file.defined = meta
	if err := toml.Unmarshal(raw, &file.extra); err != nil {
		return nil, fmt.Errorf("%s is not valid TOML: %w", path, err)
	}
	if entries, ok := file.extra["remotes"].([]map[string]any); ok {
		file.rawRemotes = entries
	} else if entries, ok := file.extra["remotes"].([]any); ok {
		for _, entry := range entries {
			if table, ok := entry.(map[string]any); ok {
				file.rawRemotes = append(file.rawRemotes, table)
			}
		}
	}
	for _, key := range known {
		remove(file.extra, key)
	}
	return file, nil
}

// Has reports whether the file actually wrote this key, dotted.
func (f *File) Has(key string) bool {
	if slices.Contains(f.undefined, key) {
		return false
	}
	return f.defined.IsDefined(strings.Split(key, ".")...)
}

// SetAutopush records a deliberate choice, so that writing off is kept and
// never having chosen stays absent from the file.
func (f *File) SetAutopush(on bool) {
	f.Store.Autopush = on
	if f.touched == nil {
		f.touched = map[string]bool{}
	}
	f.touched["store.autopush"] = true
}

// Path is the file this was read from, for messages that tell the user where to
// look.
func (f *File) Path() string { return f.path }

// Unknown lists the keys in the file that mnemo does not know, sorted. `mnemo
// config` reports them rather than ignoring them in silence: a typed key name is
// otherwise a setting the user believes they set.
func (f *File) Unknown() []string {
	var out []string
	walk(f.extra, "", &out)
	sort.Strings(out)
	return out
}

func walk(table map[string]any, prefix string, out *[]string) {
	for key, value := range table {
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		if nested, ok := value.(map[string]any); ok {
			walk(nested, path, out)
			continue
		}
		*out = append(*out, path)
	}
}

// Save writes the file back: the known settings, plus whatever else was in it.
//
// The file can hold the server's token, so it is created 0600 in a 0700
// directory, and it is replaced by a rename so a reader never sees half of it.
func (f *File) Save() error {
	if f.path == "" {
		return errors.New("this config was not loaded from a file, so there is nowhere to save it")
	}
	// A deep copy: a shallow one shares the nested tables with f.extra, and
	// writing a known key into the copy would then write it into extra too. The
	// file on disk would still be right, and the next `mnemo config` would report
	// mnemo's own settings as keys it does not recognise.
	out := clone(f.extra)
	put(out, "machine", f.Machine != "", f.Machine)
	put(out, "lang", f.Lang != "", f.Lang)
	put(out, "store.dir", f.Store.Dir != "", f.Store.Dir)
	put(out, "store.remote", f.Store.Remote != "", f.Store.Remote)
	// Off is written only when it was chosen: either the file already said so, or
	// a caller set it here. Writing it unconditionally would turn every save of
	// an unrelated setting into a decision the user never made, and would make
	// `mnemo config unset store.autopush` impossible.
	put(out, "store.autopush", f.Store.Autopush || f.Has("store.autopush") || f.touched["store.autopush"], f.Store.Autopush)
	put(out, "mailbox.dir", f.Mailbox.Dir != "", f.Mailbox.Dir)
	put(out, "server.port", f.Server.Port != 0, f.Server.Port)
	put(out, "server.token", f.Server.Token != "", f.Server.Token)
	if len(f.Remotes) > 0 {
		out["remotes"] = f.remoteTables()
	} else {
		delete(out, "remotes")
	}

	var body bytes.Buffer
	if err := toml.NewEncoder(&body).Encode(out); err != nil {
		return fmt.Errorf("writing the config: %w", err)
	}

	dir := filepath.Dir(f.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	suffix := make([]byte, 4)
	rand.Read(suffix)
	temp := filepath.Join(dir, ".config.toml."+hex.EncodeToString(suffix))
	if err := os.WriteFile(temp, body.Bytes(), 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", temp, err)
	}
	if err := os.Rename(temp, f.path); err != nil {
		os.Remove(temp)
		return fmt.Errorf("replacing %s: %w", f.path, err)
	}
	return nil
}

// remoteTables renders the remotes, each one starting from what was read so a
// field this version does not know is written back rather than dropped.
func (f *File) remoteTables() []map[string]any {
	tables := make([]map[string]any, 0, len(f.Remotes))
	for _, remote := range f.Remotes {
		table := map[string]any{}
		for _, raw := range f.rawRemotes {
			if machine, _ := raw["machine"].(string); machine != "" && machine == remote.Machine {
				table = clone(raw)
				break
			}
		}
		table["machine"] = remote.Machine
		table["ssh"] = remote.SSH
		table["server_port"] = remote.ServerPort
		table["local_port"] = remote.LocalPort
		table["token"] = remote.Token
		table["connected"] = remote.Connected
		tables = append(tables, table)
	}
	return tables
}

// clone copies a table and every table under it.
func clone(table map[string]any) map[string]any {
	out := make(map[string]any, len(table))
	for key, value := range table {
		if nested, ok := value.(map[string]any); ok {
			out[key] = clone(nested)
			continue
		}
		out[key] = value
	}
	return out
}

// put sets a dotted key, or removes it when it should not be written.
func put(table map[string]any, key string, write bool, value any) {
	if !write {
		remove(table, key)
		return
	}
	head, rest := split(key)
	if rest == "" {
		table[head] = value
		return
	}
	nested, ok := table[head].(map[string]any)
	if !ok {
		nested = map[string]any{}
		table[head] = nested
	}
	put(nested, rest, true, value)
}

// remove deletes a dotted key, and the table it was in if that leaves it empty,
// so a rewrite does not leave `[store]` with nothing under it.
func remove(table map[string]any, key string) {
	head, rest := split(key)
	if rest == "" {
		delete(table, head)
		return
	}
	nested, ok := table[head].(map[string]any)
	if !ok {
		return
	}
	remove(nested, rest)
	if len(nested) == 0 {
		delete(table, head)
	}
}

func split(key string) (head, rest string) {
	for i := 0; i < len(key); i++ {
		if key[i] == '.' {
			return key[:i], key[i+1:]
		}
	}
	return key, ""
}

// settable are the keys `mnemo config set` may write. The rest of the file is
// written by the commands that own it.
var settable = []string{"machine", "lang", "store.dir", "store.remote", "store.autopush", "mailbox.dir"}

// managed says which command owns a key a person tried to set by hand.
var managed = map[string]string{
	"server":  "mnemo server setup",
	"remotes": "mnemo connect",
}

// Settable lists the keys a person may set, in the order they are printed.
func Settable() []string { return append([]string(nil), settable...) }

// Set writes one key, validated. It does not save; the caller does, so a command
// that sets several keys writes the file once.
//
// Validation happens here rather than in the command, because a value that
// reaches the file unvalidated is read by every later command as if it were
// meant: `lang = "fr"` becomes a warning on every connection for ever.
func (f *File) Set(key, value string) error {
	if owner, ok := managed[strings.SplitN(key, ".", 2)[0]]; ok {
		return fmt.Errorf("%s is managed by %s, not by config set", key, owner)
	}
	switch key {
	case "machine":
		label := memory.NormalizeMachine(value)
		if label == "" {
			return fmt.Errorf("%q does not give a usable machine label: it must have letters or digits in it", value)
		}
		// Stored normalised, so what the file says is what every command reads.
		f.Machine = label
	case "lang":
		lang := strings.ToLower(strings.TrimSpace(value))
		if !slices.Contains(languages, lang) {
			return fmt.Errorf("lang must be one of %s, not %q", strings.Join(languages, ", "), value)
		}
		f.Lang = lang
	case "store.dir":
		f.Store.Dir = value
	case "store.remote":
		f.Store.Remote = value
	case "store.autopush":
		on, err := strconv.ParseBool(strings.ToLower(strings.TrimSpace(value)))
		if err != nil {
			return fmt.Errorf("store.autopush must be true or false, not %q", value)
		}
		f.SetAutopush(on)
	case "mailbox.dir":
		f.Mailbox.Dir = value
	default:
		return fmt.Errorf("%s is not a setting. The ones you can set are: %s",
			key, strings.Join(settable, ", "))
	}
	return nil
}

// Unset removes one key, so the default applies again.
func (f *File) Unset(key string) error {
	if owner, ok := managed[strings.SplitN(key, ".", 2)[0]]; ok {
		return fmt.Errorf("%s is managed by %s, not by config unset", key, owner)
	}
	if !slices.Contains(settable, key) {
		return fmt.Errorf("%s is not a setting. The ones you can set are: %s", key, strings.Join(settable, ", "))
	}
	switch key {
	case "machine":
		f.Machine = ""
	case "lang":
		f.Lang = ""
	case "store.dir":
		f.Store.Dir = ""
	case "store.remote":
		f.Store.Remote = ""
	case "store.autopush":
		f.Store.Autopush = false
		delete(f.touched, "store.autopush")
		f.forget("store.autopush")
	case "mailbox.dir":
		f.Mailbox.Dir = ""
	}
	return nil
}

// forget makes Has report false for a key the file used to carry, so unsetting
// autopush really removes it rather than writing false back.
func (f *File) forget(key string) {
	f.undefined = append(f.undefined, key)
}

// Mask is what a token looks like when printed. The value never appears: a
// config listing goes into terminals, transcripts and screenshots.
func Mask(value string) string {
	if value == "" {
		return ""
	}
	return "set"
}
