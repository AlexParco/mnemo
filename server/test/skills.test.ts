/** The Claude Code plugin's skills, after P6.
 *
 * Two things are asserted: that the rules they state are the shared constants and
 * not a fourth paraphrase, and that the mechanics really left — a skill that still
 * shells out to git is a skill that can disagree with the server about what the
 * store looks like. */

import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import test, { describe } from "node:test";
import * as criterion from "../src/prompts/criterion.js";
import { ROOT } from "./helpers.js";

const SKILLS_DIR = path.join(ROOT, "skills");
const SKILLS = fs.readdirSync(SKILLS_DIR).filter((d) => fs.existsSync(path.join(SKILLS_DIR, d, "SKILL.md")));
const read = (name: string) => fs.readFileSync(path.join(SKILLS_DIR, name, "SKILL.md"), "utf8");

const REGION = /<!-- mnemo:rule ([A-Z_]+) -->\n([\s\S]*?)<!-- \/mnemo:rule -->/g;

describe("the plugin still ships every command", () => {
  test("all six are present", () => {
    assert.deepEqual(SKILLS.sort(), ["forget", "list-context", "load-context", "mem", "rename", "save-context"]);
  });

  test("each declares a name and a bilingual trigger description", () => {
    for (const name of SKILLS) {
      const text = read(name);
      assert.match(text, new RegExp(`^---\\nname: ${name}\\n`), `${name} needs its frontmatter name`);
      assert.match(text, /description: .{200,}/, `${name} needs a description with trigger phrases`);
    }
  });
});

describe("the rules are the shared ones, not a fourth copy", () => {
  test("every marked region matches its constant exactly", () => {
    let regions = 0;
    for (const name of SKILLS) {
      for (const [, rule, body] of read(name).matchAll(REGION)) {
        regions++;
        const expected = (criterion as Record<string, unknown>)[rule!];
        assert.equal(typeof expected, "string", `${name}: no constant named ${rule}`);
        assert.equal(body!.trimEnd(), expected, `${name}: ${rule} has drifted — run sync-skill-rules.mjs`);
      }
    }
    assert.ok(regions >= 7, `expected the rules to be embedded in several skills, found ${regions}`);
  });

  test("the rules land where they matter", () => {
    assert.ok(read("save-context").includes(criterion.SAVE_CRITERION));
    assert.ok(read("save-context").includes(criterion.MACHINE_RULE));
    assert.ok(read("save-context").includes(criterion.CONFLICT_RULE));
    assert.ok(read("load-context").includes(criterion.MACHINE_RULE));
    assert.ok(read("mem").includes(criterion.SAVE_CRITERION));
    assert.ok(read("forget").includes(criterion.CONFIRM_NOTE));
    assert.ok(read("rename").includes(criterion.CONFIRM_NOTE));
  });
});

describe("the mechanics moved to the server", () => {
  test("no skill invokes git, or a shell, on the store", () => {
    for (const name of SKILLS) {
      const text = read(name);
      for (const pattern of [/git -C/, /git pull/, /git commit/, /git push/, /grep -rl/, /mkdir -p/]) {
        assert.ok(!pattern.test(text), `${name} still drives the store by hand: ${pattern}`);
      }
    }
  });

  test("the host-specific couplings are gone", () => {
    for (const name of SKILLS) {
      const text = read(name);
      assert.ok(!text.includes("CLAUDE_SKILL_DIR"), `${name} still resolves a path through CLAUDE_SKILL_DIR`);
      assert.ok(!text.includes("CLAUDE_PLUGIN_ROOT"), `${name} still resolves a path through CLAUDE_PLUGIN_ROOT`);
      assert.ok(!text.includes("card.py"), `${name} still calls the python card`);
    }
  });

  test("python is no longer a runtime dependency of the plugin", () => {
    assert.equal(fs.existsSync(path.join(SKILLS_DIR, "load-context", "card.py")), false);
    assert.ok(
      fs.existsSync(path.join(ROOT, "server", "test", "oracle", "card.py")),
      "it must survive as the parity oracle, though — that test is the reason the port is trustworthy",
    );
  });

  test("each skill calls the tools it needs", () => {
    const expected: Record<string, string[]> = {
      "load-context": ["mnemo_list_projects", "mnemo_sync", "mnemo_load_project"],
      "list-context": ["mnemo_list_projects"],
      "save-context": ["mnemo_sync", "mnemo_search_memories", "mnemo_write_memory", "mnemo_write_pending", "mnemo_commit", "mnemo_push"],
      mem: ["mnemo_sync", "mnemo_search_memories", "mnemo_write_memory", "mnemo_commit"],
      forget: ["mnemo_sync", "mnemo_forget"],
      rename: ["mnemo_sync", "mnemo_rename"],
    };
    for (const [name, tools] of Object.entries(expected)) {
      const text = read(name);
      for (const tool of tools) assert.ok(text.includes(tool), `${name} should call ${tool}`);
    }
  });
});

describe("the plugin wiring", () => {
  test("it ships the MCP server through a launcher, not a hardcoded path", () => {
    const mcp = JSON.parse(fs.readFileSync(path.join(ROOT, ".mcp.json"), "utf8")) as {
      mcpServers: Record<string, { command: string }>;
    };
    assert.equal(mcp.mcpServers["mnemo"]!.command, "${CLAUDE_PLUGIN_ROOT}/scripts/mnemo-mcp.sh");
    const launcher = path.join(ROOT, "scripts", "mnemo-mcp.sh");
    assert.ok(fs.existsSync(launcher));
    assert.ok((fs.statSync(launcher).mode & 0o111) !== 0, "the launcher must be executable");
  });

  test("the save reminder hook is untouched and still watches the store", () => {
    const hook = fs.readFileSync(path.join(ROOT, "scripts", "suggest-save.js"), "utf8");
    assert.match(hook, /MNEMO_DIR/);
    assert.match(hook, /storeHead/, "it resets on the store's HEAD moving — which the MCP commits still do");
  });
});
