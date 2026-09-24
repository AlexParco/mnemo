package gitx

import (
	"strings"
	"testing"
)

// Sample secrets are assembled here, never written out whole. A literal token in
// a committed file is a real token to every scanner that reads this repository,
// and it would trip push protection on the project's own remote.
var (
	githubToken      = "ghp_" + strings.Repeat("A", 30)
	githubPAT        = "github_pat_" + strings.Repeat("b", 25)
	awsKey           = "AKIA" + strings.Repeat("Q", 16)
	slackToken       = "xoxb-" + strings.Repeat("9", 12)
	privateKey       = "-----BEGIN RSA PRIVATE KEY-----"
	credentialInURL  = "https://user:" + strings.Repeat("p", 14) + "@hub.example.invalid/repo.git"
	credentialAssign = "token: " + strings.Repeat("k", 16)
)

// caught is one sample per rule. The test below fails when a rule has no sample,
// so a rule cannot be added without one.
var caught = map[string]string{
	"private key block":             privateKey,
	"AWS access key id":             awsKey,
	"GitHub token":                  githubToken,
	"GitHub fine-grained token":     githubPAT,
	"Slack token":                   slackToken,
	"URL with embedded credentials": credentialInURL,
	"credential assignment":         credentialAssign,
}

func added(lines ...string) string {
	var b strings.Builder
	b.WriteString("+++ b/memories/note.md\n@@ -0,0 +1,10 @@\n")
	for _, line := range lines {
		b.WriteString("+" + line + "\n")
	}
	return b.String()
}

func TestEveryRuleHasASampleAndCatchesIt(t *testing.T) {
	for _, rule := range Rules {
		sample, ok := caught[rule.Name]
		if !ok {
			t.Errorf("the rule %q has no sample; add one so it is known to work", rule.Name)
			continue
		}
		findings := ScanDiff(added(sample))
		var names []string
		for _, f := range findings {
			names = append(names, f.Rule)
		}
		if !contains(names, rule.Name) {
			t.Errorf("%q did not catch its own sample; it caught %q", rule.Name, names)
		}
	}
	for name := range caught {
		if !ruleExists(name) {
			t.Errorf("there is a sample for %q, but no rule by that name", name)
		}
	}
}

func ruleExists(name string) bool {
	for _, r := range Rules {
		if r.Name == name {
			return true
		}
	}
	return false
}

func contains(list []string, want string) bool {
	for _, got := range list {
		if got == want {
			return true
		}
	}
	return false
}

// A false positive here blocks the user from syncing their own memory, so the
// lines that must not fire matter as much as the ones that must.
func TestOrdinaryProseIsNotFlagged(t *testing.T) {
	clean := []string{
		"token: rotate every 15 minutes",
		"password: ask the team lead",
		"The api_key is stored in the vault, never in the repo.",
		"secret: yes",
		"Connect to https://hub.example.invalid/repo.git over SSH.",
		"AKIA is the prefix of an AWS key id.",
		"ghp_short",
		"The private key lives on the server.",
		"See xoxb- tokens in the Slack docs.",
		"decision: the cursor contract is stable",
	}
	for _, line := range clean {
		if findings := ScanDiff(added(line)); len(findings) > 0 {
			t.Errorf("%q was flagged as %q", line, findings[0].Rule)
		}
	}
}

func TestAFindingLocatesButNeverEchoes(t *testing.T) {
	findings := ScanDiff(added("aws_key = " + awsKey))
	if len(findings) == 0 {
		t.Fatal("the sample was not caught")
	}
	f := findings[0]
	if strings.Contains(f.Excerpt, awsKey) {
		t.Error("the excerpt carries the secret it is reporting")
	}
	if !strings.Contains(f.Excerpt, "[redacted") {
		t.Errorf("the excerpt does not say something was removed: %q", f.Excerpt)
	}
	if !strings.Contains(f.Excerpt, "aws_key") {
		t.Errorf("the excerpt lost the context needed to find the line: %q", f.Excerpt)
	}
	if f.File != "memories/note.md" {
		t.Errorf("file is %q, want the one in the diff header", f.File)
	}
}

// Only what the push would add is scanned. A removed line is already on the hub,
// and flagging it would block every future push over one old leak.
func TestOnlyAddedLinesAreScanned(t *testing.T) {
	diff := "+++ b/notes.md\n" +
		"@@ -1,3 +1,3 @@\n" +
		" context " + githubToken + "\n" +
		"-removed " + awsKey + "\n" +
		"+kept\n"
	if findings := ScanDiff(diff); len(findings) != 0 {
		t.Errorf("context and removed lines were flagged: %+v", findings)
	}
}

func TestLineNumbersPointAtTheFileAsPushed(t *testing.T) {
	diff := "+++ b/notes.md\n" +
		"@@ -1,2 +5,4 @@\n" +
		" first\n" +
		"-gone\n" +
		"+second\n" +
		"+third " + githubToken + "\n"
	findings := ScanDiff(diff)
	if len(findings) != 1 {
		t.Fatalf("got %d findings, want one", len(findings))
	}
	if findings[0].Line != 7 {
		t.Errorf("line is %d, want 7: the hunk starts at 5, then a context line and an added line", findings[0].Line)
	}
}

func TestEachFileInADiffIsAttributedToItself(t *testing.T) {
	diff := "+++ b/first.md\n@@ -0,0 +1 @@\n+" + githubToken + "\n" +
		"+++ b/second.md\n@@ -0,0 +1 @@\n+" + awsKey + "\n"
	findings := ScanDiff(diff)
	if len(findings) != 2 {
		t.Fatalf("got %d findings, want one per file", len(findings))
	}
	if findings[0].File != "first.md" || findings[1].File != "second.md" {
		t.Errorf("findings are attributed to %q and %q", findings[0].File, findings[1].File)
	}
}

func TestScanRangeAndPushBase(t *testing.T) {
	repo := withIdentity(t, newRepo(t))

	if _, ok := repo.PushBase(); ok {
		t.Error("a store with no commits says it has something to publish")
	}
	if findings := repo.ScanRange(); findings != nil {
		t.Errorf("a store with no commits produced findings: %+v", findings)
	}

	write(t, repo, "memories/leak.md", "---\nid: leak\n---\naws_key = "+awsKey+"\n")
	commit(t, repo, "save")

	base, ok := repo.PushBase()
	if !ok || base != emptyTree {
		t.Errorf("base is %q (%v), want the empty tree for a first push", base, ok)
	}
	findings := repo.ScanRange()
	if len(findings) != 1 || findings[0].Rule != "AWS access key id" {
		t.Fatalf("scanning the whole history gave %+v, want the leak", findings)
	}
	if findings[0].File != "memories/leak.md" {
		t.Errorf("file is %q, want the file that carries it", findings[0].File)
	}
}

func TestScanRangeAfterAPushOnlyLooksAtWhatIsNew(t *testing.T) {
	repo := withIdentity(t, newRepo(t))
	write(t, repo, "memories/old-leak.md", "aws_key = "+awsKey+"\n")
	commit(t, repo, "the leak that is already published")

	hub := New(t.TempDir())
	if _, err := hub.Run("init", "-q", "--bare"); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Run("remote", "add", "origin", hub.Dir); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Run("push", "-q", "-u", "origin", "main"); err != nil {
		t.Fatal(err)
	}

	if findings := repo.ScanRange(); len(findings) != 0 {
		t.Fatalf("an old leak already on the hub blocks new pushes: %+v", findings)
	}

	write(t, repo, "memories/new.md", "nothing secret here\n")
	commit(t, repo, "clean commit")
	if findings := repo.ScanRange(); len(findings) != 0 {
		t.Errorf("a clean commit produced findings: %+v", findings)
	}
}

func TestAcknowledgeTokenIsBoundToTheStateItWasIssuedFor(t *testing.T) {
	repo := withIdentity(t, newRepo(t))
	write(t, repo, "leak.md", "aws_key = "+awsKey+"\n")
	commit(t, repo, "save")

	findings := repo.ScanRange()
	token := repo.AcknowledgeToken(findings)
	if len(token) != 12 {
		t.Errorf("token is %q, want twelve characters", token)
	}
	if again := repo.AcknowledgeToken(findings); again != token {
		t.Error("the same state gave two different tokens")
	}
	if other := repo.AcknowledgeToken(nil); other == token {
		t.Error("a different set of findings gave the same token")
	}

	write(t, repo, "another.md", "more\n")
	commit(t, repo, "second")
	if after := repo.AcknowledgeToken(findings); after == token {
		t.Error("the token survived a new commit; it must stop working when the content changes")
	}
}
