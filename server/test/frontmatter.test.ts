/** P0 acceptance gate: parse → render is byte-identical for every fixture file,
 * and editing one field leaves every other byte untouched. Rename and forget
 * depend on this: they rewrite `projects:` and must not reflow the note. */

import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import test, { describe } from "node:test";
import { asList, parseDocument, parseFrontmatter, renderDocument, setField } from "../src/store/frontmatter.js";
import { FIXTURE_STORE } from "./helpers.js";

function allFixtureFiles(dir: string): string[] {
  return fs.readdirSync(dir, { withFileTypes: true, recursive: true })
    .filter((e) => e.isFile() && e.name.endsWith(".md"))
    .map((e) => path.join(e.parentPath, e.name));
}

describe("frontmatter round-trip", () => {
  for (const file of allFixtureFiles(FIXTURE_STORE)) {
    test(path.relative(FIXTURE_STORE, file), () => {
      const raw = fs.readFileSync(file, "utf8");
      assert.equal(renderDocument(parseDocument(raw)), raw);
    });
  }

  test("survives CRLF and a body that contains a --- rule", () => {
    const raw = "---\r\nid: x\r\nprojects: [a]\r\n---\r\n\r\nBody\r\n\r\n---\r\n\r\nMore\r\n";
    assert.equal(renderDocument(parseDocument(raw)), raw);
  });
});

describe("parseFrontmatter", () => {
  test("lists, scalars, quotes and empties", () => {
    const fm = parseFrontmatter(
      ["---", "projects: [a, 'b', \"c\"]", "name: \"Quoted\"", "empty:", "services: []", "scalar: one", "---", "body"].join("\n"),
    );
    assert.deepEqual(fm["projects"], ["a", "b", "c"]);
    assert.equal(fm["name"], "Quoted");
    assert.equal(fm["empty"], "");
    assert.deepEqual(fm["services"], []);
    assert.equal(fm["scalar"], "one");
  });

  test("a scalar projects value reads as a one-element list", () => {
    const fm = parseFrontmatter("---\nprojects: solo\n---\n");
    assert.deepEqual(asList(fm, "projects"), ["solo"]);
    assert.deepEqual(asList(fm, "missing"), []);
  });

  test("no frontmatter, or an unterminated block, yields nothing", () => {
    assert.deepEqual(parseFrontmatter("just a body\n"), {});
    assert.deepEqual(parseFrontmatter("---\nid: x\nnever closed\n"), {});
  });
});

describe("setField", () => {
  const raw = [
    "---",
    "id: note",
    "projects: [alpha, beta]   ",
    "  indented: yes",
    "# a comment line the parser ignores",
    "type: decision",
    "---",
    "",
    "Body with projects: [alpha] in the prose.",
    "",
  ].join("\n");

  test("rewrites only the target line", () => {
    const out = renderDocument(setField(parseDocument(raw), "projects", ["gamma", "beta"]));
    assert.equal(out, raw.replace("projects: [alpha, beta]   ", "projects: [gamma, beta]"));
    assert.ok(out.includes("Body with projects: [alpha] in the prose."), "body prose must survive");
    assert.ok(out.includes("# a comment line the parser ignores"), "unknown lines must survive");
  });

  test("preserves indentation and appends a missing key", () => {
    assert.ok(renderDocument(setField(parseDocument(raw), "indented", "no")).includes("  indented: no"));
    const added = renderDocument(setField(parseDocument(raw), "updated", "2026-08-27"));
    assert.ok(added.includes("type: decision\nupdated: 2026-08-27\n---"));
  });
});
