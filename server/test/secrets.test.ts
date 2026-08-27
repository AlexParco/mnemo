/** The secret corpus. A false negative here is the worst bug in the repo: it is
 * the difference between a credential staying on the machine and being published
 * to a hub that may well be a third-party host.
 *
 * The sample secrets are assembled at runtime rather than written out. A literal
 * 40-character `ghp_…` in a committed test file is a real token as far as a
 * scanner is concerned, and would trip push protection on mnemo's own repo. */

import assert from "node:assert/strict";
import test, { describe } from "node:test";
import { scanDiff, RULES } from "../src/git/secrets.js";

const filler = (n: number) => "a1B2c3D4e5F6".repeat(Math.ceil(n / 12)).slice(0, n);
const SAMPLES: Record<string, string> = {
  "private key block": "-" .repeat(5) + "BEGIN RSA PRIVATE KEY" + "-".repeat(5),
  "AWS access key id": "AKIA" + "IOSFODNN7EXAMPLE",
  "GitHub token": "ghp" + "_" + filler(36),
  "GitHub fine-grained token": "github" + "_pat_" + filler(30),
  "Slack token": "xox" + "b-" + "123456789012-" + filler(12),
  "URL with embedded credentials": "postgres://admin:" + filler(14) + "@db.internal:5432/app",
  "credential assignment": "api_key: sk" + "_live_" + filler(20),
};

/** A one-file diff that adds each given line. */
function diffAdding(lines: string[], file = "memories/note.md"): string {
  return [`--- a/${file}`, `+++ b/${file}`, `@@ -1,1 +1,${lines.length} @@`, ...lines.map((l) => `+${l}`)].join("\n");
}

describe("every rule has a sample, and every sample is caught", () => {
  test("the corpus covers the whole rule set", () => {
    assert.deepEqual(Object.keys(SAMPLES).sort(), RULES.map((r) => r.name).sort());
  });

  for (const [rule, sample] of Object.entries(SAMPLES)) {
    test(rule, () => {
      const findings = scanDiff(diffAdding([`Some note text ${sample} trailing words`]));
      assert.ok(findings.some((f) => f.rule === rule), `${rule} was not caught`);
    });
  }

  test("a secret is located but never echoed back", () => {
    const secret = SAMPLES["GitHub token"]!;
    const [finding] = scanDiff(diffAdding([`the token is ${secret}`]));
    assert.equal(finding!.file, "memories/note.md");
    assert.ok(!finding!.excerpt.includes(secret), "the finding must not carry the secret");
    assert.match(finding!.excerpt, /\[redacted \d+ chars: /);
  });
});

describe("what must NOT be flagged", () => {
  // Real lines from a store of engineering prose. Measured against the plugin's
  // shell grep, one of these ten fires: `token: rotate every 15 minutes`. A false
  // positive is a hard block on syncing memory, so the refined rule earns itself.
  const PROSE = [
    "Access tokens rotate every 15 minutes; refresh tokens are single use.",
    "token: rotate every 15 minutes",
    "The secret is that the workers retry silently.",
    "See https://example.invalid/docs/auth for the flow.",
    "password reset flow is broken on Safari",
    "api_key must never be logged, not even at debug level",
    "id: orion-token-rotation",
    "projects: [orion-api, atlas-web]",
    "updated: 2026-08-27",
    "Link the decision with [[orion-auth-rotation]].",
  ];

  for (const line of PROSE) {
    test(JSON.stringify(line.slice(0, 48)), () => {
      assert.deepEqual(scanDiff(diffAdding([line])), []);
    });
  }
});

describe("scanning is scoped to what would be published", () => {
  const secret = SAMPLES["AWS access key id"]!;

  test("removed lines are not flagged: they are already on the hub", () => {
    const diff = ["--- a/memories/n.md", "+++ b/memories/n.md", "@@ -1,2 +1,1 @@", `-key ${secret}`, "+key removed"].join("\n");
    assert.deepEqual(scanDiff(diff), []);
  });

  test("context lines are not flagged either", () => {
    const diff = ["--- a/memories/n.md", "+++ b/memories/n.md", "@@ -1,2 +1,2 @@", ` key ${secret}`, "+a new line"].join("\n");
    assert.deepEqual(scanDiff(diff), []);
  });

  test("line numbers point at the file as it would be pushed", () => {
    const diff = [
      "--- a/memories/n.md", "+++ b/memories/n.md",
      "@@ -1,3 +1,4 @@", " first", " second", `+leaked ${secret}`, " third",
    ].join("\n");
    const [finding] = scanDiff(diff);
    assert.equal(finding!.line, 3);
  });

  test("each file in a multi-file diff is attributed correctly", () => {
    const diff = [
      "--- a/memories/a.md", "+++ b/memories/a.md", "@@ -0,0 +1,1 @@", "+clean line",
      "--- a/memories/b.md", "+++ b/memories/b.md", "@@ -0,0 +1,1 @@", `+leaked ${secret}`,
    ].join("\n");
    const findings = scanDiff(diff);
    assert.equal(findings.length, 1);
    assert.equal(findings[0]!.file, "memories/b.md");
  });
});
