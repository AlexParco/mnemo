/** The executable, as a process.
 *
 * Everything else in this suite imports the modules. That is exactly how the
 * symlink bug survived: the code was correct when imported and dead when run.
 * These tests only ever spawn. */

import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import { spawn, spawnSync } from "node:child_process";
import test, { after, describe } from "node:test";
import { renderCard } from "../src/card/render.js";
import { cleanupTempDirs, FIXTURE_STORE, isolateEnv, tempDir } from "./helpers.js";

isolateEnv();
after(cleanupTempDirs);

const BIN = path.resolve(import.meta.dirname, "..", "src", "bin.js");
const CARD_CLI = path.resolve(import.meta.dirname, "..", "src", "card", "cli.js");

const INITIALIZE = JSON.stringify({
  jsonrpc: "2.0",
  id: 1,
  method: "initialize",
  params: { protocolVersion: "2025-06-18", capabilities: {}, clientInfo: { name: "bin-test", version: "0" } },
});

/** Spawn a command, send one initialize, and resolve with its reply. Rejects if
 * the process exits without answering — which is what a server that never
 * started looks like from the outside. */
function initialize(command: string, args: string[]): Promise<{ name: string; version: string }> {
  return new Promise((resolve, reject) => {
    const child = spawn(command, args, {
      stdio: ["pipe", "pipe", "pipe"],
      env: { ...process.env, MNEMO_DIR: FIXTURE_STORE, MNEMO_MACHINE: "fixture-box" },
    });
    const timer = setTimeout(() => {
      child.kill();
      reject(new Error("no reply within 10s"));
    }, 10_000);
    let out = "";
    let err = "";
    child.stdout.on("data", (chunk: Buffer) => {
      out += chunk.toString();
      const line = out.split("\n").find((l) => l.trim() !== "");
      if (!line) return;
      clearTimeout(timer);
      child.kill();
      try {
        resolve((JSON.parse(line) as { result: { serverInfo: { name: string; version: string } } }).result.serverInfo);
      } catch (parseErr) {
        reject(parseErr as Error);
      }
    });
    child.stderr.on("data", (chunk: Buffer) => (err += chunk.toString()));
    child.on("error", reject);
    child.on("close", (code) => {
      clearTimeout(timer);
      reject(new Error(`exited ${code} without replying. stderr: ${err.trim() || "(empty)"}`));
    });
    child.stdin.write(INITIALIZE + "\n");
  });
}

describe("the mnemo-mcp executable", () => {
  test("answers initialize when run by its real path", async () => {
    assert.deepEqual(await initialize(process.execPath, [BIN]), { name: "mnemo", version: "0.1.0" });
  });

  test("answers initialize when run through a symlink", async () => {
    // This is the shape npm gives a `bin`: node_modules/.bin/<name> links to the
    // real file. The old entry point compared import.meta.url against argv[1],
    // which differ here, so it started and silently did nothing.
    const link = path.join(tempDir("mnemo-bin-"), "mnemo-mcp");
    fs.symlinkSync(BIN, link);
    assert.deepEqual(await initialize(process.execPath, [link]), { name: "mnemo", version: "0.1.0" });
  });

  test("answers initialize from a directory whose path has spaces", async () => {
    const dir = path.join(tempDir("mnemo-bin-"), "a folder with spaces");
    fs.mkdirSync(dir);
    const link = path.join(dir, "mnemo-mcp");
    fs.symlinkSync(BIN, link);
    assert.deepEqual(await initialize(process.execPath, [link]), { name: "mnemo", version: "0.1.0" });
  });

  test("importing the module starts nothing", async () => {
    // The reason the old guard existed. It has to keep holding without one.
    const probe = "import('file://" + path.resolve(import.meta.dirname, "..", "src", "index.js") + "').then(m => { console.log(typeof m.createServer); })";
    const res = spawnSync(process.execPath, ["-e", probe], { encoding: "utf8", timeout: 10_000 });
    assert.equal(res.status, 0, res.stderr);
    assert.equal(res.stdout.trim(), "function");
  });
});

describe("the card CLI, a drop-in for the python original", () => {
  test("prints what renderCard renders, plus a newline", () => {
    const res = spawnSync(process.execPath, [CARD_CLI, "orion-api", "es"], {
      encoding: "utf8",
      env: { ...process.env, MNEMO_DIR: FIXTURE_STORE, MNEMO_MACHINE: "fixture-box" },
    });
    assert.equal(res.status, 0, res.stderr);
    assert.equal(res.stdout, renderCard(FIXTURE_STORE, "orion-api", { lang: "es", machine: "fixture-box" }) + "\n");
  });

  test("keeps the original's exit codes", () => {
    const env = { ...process.env, MNEMO_DIR: FIXTURE_STORE };
    const missing = spawnSync(process.execPath, [CARD_CLI, "no-such-project"], { encoding: "utf8", env });
    assert.equal(missing.status, 1);
    assert.match(missing.stderr, /project 'no-such-project' not found/);
    const noArgs = spawnSync(process.execPath, [CARD_CLI], { encoding: "utf8", env });
    assert.equal(noArgs.status, 2);
    assert.match(noArgs.stderr, /usage: card <slug> \[lang\]/);
  });
});
