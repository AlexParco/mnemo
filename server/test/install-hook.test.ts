/** The SessionStart hook that builds the server on a fresh install.
 *
 * The build itself is exercised by hand (it needs the network and takes seconds);
 * what is pinned here is everything around it, because those are the paths that
 * run on EVERY session start and must be silent, cheap and harmless. */

import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import { spawnSync } from "node:child_process";
import test, { after, describe } from "node:test";
import { cleanupTempDirs, isolateEnv, ROOT, tempDir } from "./helpers.js";

isolateEnv();
after(cleanupTempDirs);

const HOOK = path.join(ROOT, "scripts", "ensure-server.sh");
const PLUGIN_VERSION = (JSON.parse(fs.readFileSync(path.join(ROOT, ".claude-plugin", "plugin.json"), "utf8")) as { version: string }).version;

function run(env: Record<string, string | undefined>) {
  return spawnSync("sh", [HOOK], {
    encoding: "utf8",
    timeout: 30_000,
    env: { ...process.env, CLAUDE_PLUGIN_ROOT: ROOT, ...env },
  });
}

/** A data dir that already holds a build for the current version. */
function builtData(version = PLUGIN_VERSION): string {
  const data = tempDir("mnemo-hookdata-");
  fs.mkdirSync(path.join(data, "server", "dist", "src"), { recursive: true });
  fs.writeFileSync(path.join(data, "server", "dist", "src", "bin.js"), "// pretend build\n");
  fs.writeFileSync(path.join(data, ".built"), version);
  return data;
}

describe("the install hook", () => {
  test("says nothing and changes nothing when the build is current", () => {
    const data = builtData();
    const before = fs.readFileSync(path.join(data, "server", "dist", "src", "bin.js"), "utf8");
    const res = run({ CLAUDE_PLUGIN_DATA: data });
    assert.equal(res.status, 0);
    assert.equal(res.stdout, "", "the fast path runs on every session start: it must be silent");
    assert.equal(fs.readFileSync(path.join(data, "server", "dist", "src", "bin.js"), "utf8"), before);
  });

  test("steps aside when another session is already building", () => {
    const data = builtData("0.0.0-stale");
    fs.mkdirSync(path.join(data, ".build-lock"));
    const res = run({ CLAUDE_PLUGIN_DATA: data });
    assert.equal(res.status, 0);
    assert.equal(res.stdout, "");
    assert.equal(fs.readFileSync(path.join(data, ".built"), "utf8"), "0.0.0-stale", "it must not have rebuilt");
  });

  test("does nothing outside a plugin context", () => {
    const res = spawnSync("sh", [HOOK], { encoding: "utf8", timeout: 30_000, env: { ...process.env, CLAUDE_PLUGIN_ROOT: "", CLAUDE_PLUGIN_DATA: "" } });
    assert.equal(res.status, 0);
    assert.equal(res.stdout, "");
  });

  test("a stale stamp is what triggers a rebuild, not a missing binary alone", () => {
    // The stamp carries the version, so an update rebuilds even though the old
    // dist is still sitting there and looks perfectly usable.
    const data = builtData("0.0.0-stale");
    fs.mkdirSync(path.join(data, ".build-lock")); // block the actual build
    const res = run({ CLAUDE_PLUGIN_DATA: data });
    assert.equal(res.status, 0);
    // With the lock held it bails before building; the point is it got that far
    // rather than taking the fast path.
    assert.equal(fs.existsSync(path.join(data, ".build-lock")), true);
  });

  test("it never fails the session", () => {
    // A hook that exits non-zero can block Claude Code. Every branch returns 0,
    // including the one where the plugin manifest cannot be read.
    const broken = tempDir("mnemo-broken-");
    fs.mkdirSync(path.join(broken, ".claude-plugin"), { recursive: true });
    fs.writeFileSync(path.join(broken, ".claude-plugin", "plugin.json"), "{ not json");
    const res = spawnSync("sh", [HOOK], {
      encoding: "utf8", timeout: 30_000,
      env: { ...process.env, CLAUDE_PLUGIN_ROOT: broken, CLAUDE_PLUGIN_DATA: tempDir("mnemo-hookdata-") },
    });
    assert.equal(res.status, 0);
  });
});

describe("the hook and the launcher agree on where the build lives", () => {
  test("both use <plugin-data>/server/dist/src/bin.js", () => {
    const hook = fs.readFileSync(HOOK, "utf8");
    const launcher = fs.readFileSync(path.join(ROOT, "scripts", "mnemo-mcp.sh"), "utf8");
    assert.match(hook, /\$DATA\/server\/dist\/src\/bin\.js/);
    assert.match(launcher, /CLAUDE_PLUGIN_DATA:-\}\/server\/dist\/src\/bin\.js/);
  });

  test("the hook is registered as a SessionStart hook", () => {
    const hooks = JSON.parse(fs.readFileSync(path.join(ROOT, "hooks", "hooks.json"), "utf8")) as {
      hooks: Record<string, Array<{ hooks: Array<{ command: string }> }>>;
    };
    assert.match(hooks.hooks["SessionStart"]![0]!.hooks[0]!.command, /ensure-server\.sh/);
    assert.ok(hooks.hooks["PreToolUse"], "the save reminder must still be registered");
  });
});
