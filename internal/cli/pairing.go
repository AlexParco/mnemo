package cli

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// The pairing code: everything another machine needs to reach this one, in one
// string somebody can copy.
//
// It carries the token, so it is a password and is treated like one — printed
// once by `server setup` and by `server rotate`, never logged, and never stored
// anywhere but the client's own config after `connect`.

// codePrefix names the format so that a code from a later version is refused
// with a sentence rather than a decoding error.
const codePrefix = "mnemo1_"

// pairing is what a code carries.
type pairing struct {
	Machine string `json:"machine"`
	Port    int    `json:"port"`
	Token   string `json:"token"`
}

// code is the pairing as the string a person copies.
func (p pairing) code() string {
	// Field order is the struct's, which is why this is a struct and not a map:
	// a map would reorder and two codes for the same server would look
	// different, which somebody comparing them by eye would read as a problem.
	encoded, err := json.Marshal(p)
	if err != nil {
		// Three strings and an integer cannot fail to encode.
		panic("encoding a pairing code: " + err.Error())
	}
	return codePrefix + base64.RawURLEncoding.EncodeToString(encoded)
}

// readCode turns a pasted code back into what it carries.
//
// Everything that can be wrong with it is wrong in a way the person can fix, so
// each is its own sentence: a code from somewhere else, a code that was cut in
// half by a terminal, a code from a version that writes them differently.
func readCode(given string) (pairing, error) {
	var carried pairing
	given = strings.TrimSpace(given)
	rest, ok := strings.CutPrefix(given, codePrefix)
	if !ok {
		return carried, usage("that is not a mnemo pairing code: one starts with %s. Run mnemo server "+
			"setup on the machine that keeps the memory, and paste what it prints.", codePrefix)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(rest)
	if err != nil {
		return carried, usage("that pairing code is damaged — it may have been cut short when it was " +
			"copied. Paste the whole of it, or run mnemo server rotate for a new one.")
	}
	if err := json.Unmarshal(decoded, &carried); err != nil {
		return carried, usage("that pairing code is not one this version understands. Run mnemo update on " +
			"both machines so they are the same version.")
	}
	switch {
	case carried.Machine == "":
		return carried, usage("that pairing code does not say which machine it is for")
	case carried.Port <= 0 || carried.Port > 65535:
		return carried, usage("that pairing code names port %d, which is not a port", carried.Port)
	case carried.Token == "":
		return carried, usage("that pairing code carries no token, so it cannot reach anything")
	}
	return carried, nil
}

// printCode is how a code is handed over: once, with what to do with it, and
// with a word about what it is.
func printCode(out io.Writer, carried pairing) error {
	_, err := fmt.Fprintf(out, "  Pairing code for %s — it carries the token, so treat it like a "+
		"password:\n\n    %s\n\n  On the other machine, run:\n\n    mnemo connect %s\n\n"+
		"  It is printed once. mnemo server rotate prints a new one and stops the old.\n",
		carried.Machine, carried.code(), carried.code())
	return err
}
