#!/usr/bin/env node
// mnemo — PreToolUse hook (Edit|Write) that suggests /mnemo:save-context when
// unsaved work piles up. It does NOT block the edit; it only leaves a nudge.
//
// Primary signal: edits since the last save. The hook watches the mnemo store's
// HEAD; when it changes (you ran /mnemo:save-context or /mnemo:mem) the counter
// resets and the hook goes quiet. It measures UNSAVED work, not context size.
//
// Secondary signal (optional, off by default): context size in tokens, read from
// the transcript. The transcript format is internal to Claude Code and may change
// between versions, so it stays off unless you enable it.
//
// Config (env):
//   MNEMO_DIR              store path              (default: ~/.local/share/mnemo)
//   MNEMO_SAVE_EDITS       edits before nudging, and how often to re-nudge
//                          (default: 40; 0 disables it)
//   MNEMO_SAVE_TOKENS      context token threshold (default: 0 = off)
//   MNEMO_SAVE_TOKENS_STEP re-nudge step by tokens (default: 60000)
//   MNEMO_LANG             message language, "en" (default) or "es"
//
// Never throws: on any error it exits 0 silently. A broken hook must not get in
// the way of your work.

const fs = require("fs");
const path = require("path");
const os = require("os");

function main() {
  let stdin = "";
  try {
    stdin = fs.readFileSync(0, "utf8");
  } catch {
    return; // no stdin, nothing to do
  }

  let input;
  try {
    input = JSON.parse(stdin);
  } catch {
    return;
  }

  const sessionId = input.session_id;
  if (!sessionId) return;

  const store =
    process.env.MNEMO_DIR ||
    path.join(
      process.env.XDG_DATA_HOME || path.join(os.homedir(), ".local", "share"),
      "mnemo"
    );
  // If the store doesn't exist, mnemo isn't installed: don't bother.
  if (!isDir(store)) return;

  const editThreshold = intEnv("MNEMO_SAVE_EDITS", 40);
  const tokenThreshold = intEnv("MNEMO_SAVE_TOKENS", 0);
  const tokenStep = intEnv("MNEMO_SAVE_TOKENS_STEP", 60000);
  const es = (process.env.MNEMO_LANG || "en").trim().toLowerCase().startsWith("es");

  const state = loadState(sessionId);

  // --- reset on save: if the store's HEAD changed, the user persisted something ---
  const head = storeHead(store);
  if (head && state.storeHead && head !== state.storeHead) {
    state.editsAtLastNudge = state.edits; // back to "0 unsaved edits"
    state.tokensAtLastNudge = 0;
  }
  if (head) state.storeHead = head;

  // --- edits signal ---
  state.edits = (state.edits || 0) + 1;
  const unsaved = state.edits - (state.editsAtLastNudge || 0);

  let nudge = false;
  let reason = "";

  if (editThreshold > 0 && unsaved >= editThreshold) {
    nudge = true;
    reason = es
      ? `${unsaved} ediciones sin guardar`
      : `${unsaved} unsaved edit${unsaved === 1 ? "" : "s"}`;
    state.editsAtLastNudge = state.edits;
  }

  // --- optional context signal ---
  if (!nudge && tokenThreshold > 0 && input.transcript_path) {
    const tokens = contextTokens(input.transcript_path);
    if (
      tokens != null &&
      tokens >= tokenThreshold &&
      tokens - (state.tokensAtLastNudge || 0) >= tokenStep
    ) {
      nudge = true;
      const k = Math.round(tokens / 1000);
      reason = es ? `contexto en ~${k}k tokens` : `context at ~${k}k tokens`;
      state.tokensAtLastNudge = tokens;
    }
  }

  saveState(sessionId, state);

  if (!nudge) return;

  const msg = es
    ? `📝 mnemo: ${reason} en esta sesión. Considera /mnemo:save-context <proyecto> ` +
      `para no perder el avance.`
    : `📝 mnemo: ${reason} this session. Consider /mnemo:save-context <project> ` +
      `so you don't lose progress.`;

  process.stdout.write(
    JSON.stringify({
      systemMessage: msg,
      hookSpecificOutput: {
        hookEventName: "PreToolUse",
        additionalContext: msg,
      },
    })
  );
}

// --- helpers ---

function isDir(p) {
  try {
    return fs.statSync(p).isDirectory();
  } catch {
    return false;
  }
}

function intEnv(name, dflt) {
  const v = process.env[name];
  if (v == null || v === "") return dflt;
  const n = parseInt(v, 10);
  return Number.isFinite(n) ? n : dflt;
}

function stateDir() {
  const base =
    process.env.XDG_STATE_HOME || path.join(os.homedir(), ".local", "state");
  return path.join(base, "mnemo", "suggest-save");
}

function loadState(sessionId) {
  try {
    const raw = fs.readFileSync(
      path.join(stateDir(), `${sessionId}.json`),
      "utf8"
    );
    return JSON.parse(raw) || {};
  } catch {
    return {};
  }
}

function saveState(sessionId, state) {
  try {
    const dir = stateDir();
    fs.mkdirSync(dir, { recursive: true });
    fs.writeFileSync(
      path.join(dir, `${sessionId}.json`),
      JSON.stringify(state)
    );
    pruneOldState(dir);
  } catch {
    // the nudge isn't worth an exception
  }
}

// Clean up state from old sessions (>7 days) so files don't pile up.
function pruneOldState(dir) {
  try {
    const cutoff = Date.now() - 7 * 24 * 60 * 60 * 1000;
    for (const f of fs.readdirSync(dir)) {
      const p = path.join(dir, f);
      if (fs.statSync(p).mtimeMs < cutoff) fs.unlinkSync(p);
    }
  } catch {
    // best-effort
  }
}

// Store HEAD SHA without spawning git (direct read of .git).
function storeHead(store) {
  try {
    const gitDir = path.join(store, ".git");
    const head = fs.readFileSync(path.join(gitDir, "HEAD"), "utf8").trim();
    if (!head.startsWith("ref:")) return head; // detached
    const ref = head.slice(4).trim();
    const loose = path.join(gitDir, ref);
    if (fs.existsSync(loose)) return fs.readFileSync(loose, "utf8").trim();
    const packed = path.join(gitDir, "packed-refs");
    if (fs.existsSync(packed)) {
      for (const line of fs.readFileSync(packed, "utf8").split("\n")) {
        if (line.endsWith(" " + ref)) return line.split(" ")[0];
      }
    }
    return null;
  } catch {
    return null;
  }
}

// Context size of the latest turn = input + cache_read + cache_creation.
// Claude Code internal format; may break between versions (hence opt-in).
function contextTokens(transcriptPath) {
  try {
    const lines = fs
      .readFileSync(transcriptPath, "utf8")
      .split("\n")
      .filter(Boolean);
    for (let i = lines.length - 1; i >= 0; i--) {
      let o;
      try {
        o = JSON.parse(lines[i]);
      } catch {
        continue;
      }
      const u = (o && o.message && o.message.usage) || (o && o.usage);
      if (u && (u.input_tokens != null || u.cache_read_input_tokens != null)) {
        return (
          (u.input_tokens || 0) +
          (u.cache_read_input_tokens || 0) +
          (u.cache_creation_input_tokens || 0)
        );
      }
    }
  } catch {
    // unreadable transcript: token signal simply off
  }
  return null;
}

main();
