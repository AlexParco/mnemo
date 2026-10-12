package cli

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// The pairing code is the one thing a person copies between machines, and it
// carries the token — so every way it can be wrong has to come back as a
// sentence they can act on rather than as a decoding error.
func TestAPairingCodeCarriesTheServerAndRefusesEveryWayOfBeingWrong(t *testing.T) {
	made := pairing{Machine: "vps", Port: 7433, Token: "a-token-from-the-server"}
	code := made.code()

	if !strings.HasPrefix(code, codePrefix) {
		t.Errorf("the code is %q, which does not name its format", code)
	}
	// Base64url without padding: a code with a `+`, a `/` or a `=` in it is one
	// that breaks when it is pasted into a shell or a chat window.
	for _, wrong := range []string{"+", "/", "="} {
		if strings.Contains(code, wrong) {
			t.Errorf("the code contains %q: %s", wrong, code)
		}
	}

	back, err := readCode(code)
	if err != nil {
		t.Fatalf("a code this version wrote: %v", err)
	}
	if back != made {
		t.Errorf("it came back as %+v, want %+v", back, made)
	}
	// Spaces around it are what a terminal adds when somebody copies a line.
	if spaced, err := readCode("  " + code + "\n"); err != nil || spaced != made {
		t.Errorf("a code with whitespace around it: %+v %v", spaced, err)
	}

	for what, given := range map[string]string{
		"nothing":                 "",
		"a sentence":              "please connect me",
		"a code without a prefix": base64.RawURLEncoding.EncodeToString([]byte(`{"machine":"vps"}`)),
		"a code cut short":        code[:len(code)-8],
		"a code that is not JSON": codePrefix + base64.RawURLEncoding.EncodeToString([]byte("not json")),
		"a code with no machine": codePrefix + base64.RawURLEncoding.EncodeToString(
			[]byte(`{"port":7433,"token":"t"}`)),
		"a code with no token": codePrefix + base64.RawURLEncoding.EncodeToString(
			[]byte(`{"machine":"vps","port":7433}`)),
		"a code with a port that is not one": codePrefix + base64.RawURLEncoding.EncodeToString(
			[]byte(`{"machine":"vps","port":99999,"token":"t"}`)),
	} {
		carried, err := readCode(given)
		if err == nil {
			t.Errorf("%s was accepted as %+v", what, carried)
			continue
		}
		// A usage error, not a failure: the person typed something, and the
		// exit status is what a script reads to tell the two apart.
		var wrong usageError
		if !asUsage(err, &wrong) {
			t.Errorf("%s came back as %T rather than as something the person can fix", what, err)
		}
	}
}

// The token is the whole of what protects a store, so it comes from the
// operating system and nothing about it is derived from anything.
func TestATokenIsThirtyTwoRandomBytesAndNeverTheSameTwice(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		token, err := newToken()
		if err != nil {
			t.Fatal(err)
		}
		if seen[token] {
			t.Fatalf("the same token twice: %s", token)
		}
		seen[token] = true

		decoded, err := base64.RawURLEncoding.DecodeString(token)
		if err != nil {
			t.Fatalf("a token that is not base64url: %q", token)
		}
		if len(decoded) != 32 {
			t.Errorf("a token of %d bytes", len(decoded))
		}
	}
}

// A code is read by a different version than the one that wrote it, so the
// shape is part of the contract rather than an implementation detail.
func TestTheCodeIsTheShapeTheSpecGives(t *testing.T) {
	code := pairing{Machine: "vps", Port: 7433, Token: "abc"}.code()
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(code, codePrefix))
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(decoded, &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 3 {
		t.Errorf("the code carries %v, and the spec gives three fields", fields)
	}
	for _, name := range []string{"machine", "port", "token"} {
		if _, there := fields[name]; !there {
			t.Errorf("the code has no %q: %v", name, fields)
		}
	}
	// The port is a number, not a string: a client reading it with a parser
	// would otherwise fail on a code from this version.
	if _, ok := fields["port"].(float64); !ok {
		t.Errorf("the port is %T", fields["port"])
	}
}

// asUsage is errors.As, named for the question it asks: is this the person's
// typing rather than mnemo failing.
func asUsage(err error, into *usageError) bool {
	return errors.As(err, into)
}
