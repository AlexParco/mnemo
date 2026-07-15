#!/usr/bin/env node
// mnemo — hook PreToolUse (Edit|Write) que sugiere /mnemo:save-context cuando se acumula
// trabajo sin persistir. NO bloquea la edición; solo deja un aviso.
//
// Señal primaria: ediciones desde el último guardado. El hook mira el HEAD del
// store de mnemo; cuando cambia (corriste /mnemo:save-context o /mnemo:mem), el contador se
// reinicia solo y el hook se calla. Mide trabajo SIN persistir, no tamaño de contexto.
//
// Señal secundaria (opcional, off por default): tamaño de contexto en tokens,
// leído del transcript. El formato del transcript es interno de Claude Code y puede
// cambiar entre versiones, por eso va apagado salvo que lo actives.
//
// Config (env):
//   MNEMO_DIR              ruta del store          (default: ~/.local/share/mnemo)
//   MNEMO_SAVE_EDITS       ediciones antes de avisar, y cada cuántas re-avisar
//                          (default: 40; 0 lo desactiva)
//   MNEMO_SAVE_TOKENS      umbral de contexto en tokens (default: 0 = off)
//   MNEMO_SAVE_TOKENS_STEP re-aviso por tokens        (default: 60000)
//
// Nunca lanza: ante cualquier error sale 0 sin avisar. Un hook roto no debe
// entorpecer tu trabajo.

const fs = require("fs");
const path = require("path");
const os = require("os");

function main() {
  let stdin = "";
  try {
    stdin = fs.readFileSync(0, "utf8");
  } catch {
    return; // sin stdin no hay nada que hacer
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
  // Si el store no existe, mnemo no está instalado: no molestamos.
  if (!isDir(store)) return;

  const editThreshold = intEnv("MNEMO_SAVE_EDITS", 40);
  const tokenThreshold = intEnv("MNEMO_SAVE_TOKENS", 0);
  const tokenStep = intEnv("MNEMO_SAVE_TOKENS_STEP", 60000);

  const state = loadState(sessionId);

  // --- reset al guardar: si el HEAD del store cambió, el usuario persistió algo ---
  const head = storeHead(store);
  if (head && state.storeHead && head !== state.storeHead) {
    state.editsAtLastNudge = state.edits; // "0 ediciones sin guardar" otra vez
    state.tokensAtLastNudge = 0;
  }
  if (head) state.storeHead = head;

  // --- señal de ediciones ---
  state.edits = (state.edits || 0) + 1;
  const unsaved = state.edits - (state.editsAtLastNudge || 0);

  let nudge = false;
  let reason = "";

  if (editThreshold > 0 && unsaved >= editThreshold) {
    nudge = true;
    reason = `${unsaved} ediciones sin guardar`;
    state.editsAtLastNudge = state.edits;
  }

  // --- señal opcional de contexto ---
  if (!nudge && tokenThreshold > 0 && input.transcript_path) {
    const tokens = contextTokens(input.transcript_path);
    if (
      tokens != null &&
      tokens >= tokenThreshold &&
      tokens - (state.tokensAtLastNudge || 0) >= tokenStep
    ) {
      nudge = true;
      reason = `contexto en ~${Math.round(tokens / 1000)}k tokens`;
      state.tokensAtLastNudge = tokens;
    }
  }

  saveState(sessionId, state);

  if (!nudge) return;

  const msg =
    `📝 mnemo: ${reason} en esta sesión. Considera /mnemo:save-context <proyecto> ` +
    `para no perder el avance (nada se sube sin tu confirmación).`;

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
    // el aviso no vale una excepción
  }
}

// Limpia estados de sesiones viejas (>7 días) para que no se acumulen.
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

// SHA del HEAD del store sin lanzar git (lectura directa de .git).
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

// Tamaño de contexto del último turno = input + cache_read + cache_creation.
// Formato interno de Claude Code; puede romper entre versiones (por eso es opt-in).
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
    // transcript ilegible: señal de tokens simplemente off
  }
  return null;
}

main();
