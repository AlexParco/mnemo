#!/usr/bin/env bash
# Instala el ENGINE de mnemo:
#   1) enlaza los skills en ~/.claude/skills
#   2) crea (o repara) tu DATA STORE: un repo git aparte
#
# El engine (esto) se comparte en GitHub. El store (tu data) vive donde vos decidas:
# un bare repo en tu VPS, un repo privado en GitHub/GitLab, un Gitea self-hosted, o
# solo en esta máquina. El engine no opina sobre el servidor: le basta con que haya
# un remoto git, o ninguno.
#
#   MNEMO_DIR     ruta del store   (default: ~/.local/share/mnemo)
#   MNEMO_REMOTE  URL del remoto   (opcional; ej. git@mi-vps:mnemo.git)
#   MNEMO_HOOK=1  instala el hook que sugiere /save-context en sesiones largas
#                 (opcional; modifica ~/.claude/settings.json. Sin esto, solo
#                  imprime el snippet para que lo agregues a mano)
#
# Idempotente: correlo las veces que quieras, en cualquier máquina.
set -euo pipefail

ENGINE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SKILLS_SRC="$ENGINE_DIR/skills"
SKILLS_DST="$HOME/.claude/skills"
STORE_DIR="${MNEMO_DIR:-${XDG_DATA_HOME:-$HOME/.local/share}/mnemo}"
REMOTE="${MNEMO_REMOTE:-}"
SETTINGS="$HOME/.claude/settings.json"
HOOK_JS="$ENGINE_DIR/skills/save-context/suggest-save.js"
HOOK_CMD="node $HOOK_JS"

# --- 1. enlazar skills ---
mkdir -p "$SKILLS_DST"
for skill in "$SKILLS_SRC"/*/; do
  name="$(basename "$skill")"
  target="$SKILLS_DST/$name"
  if [ -e "$target" ] && [ ! -L "$target" ]; then
    echo "⚠  $target existe y NO es symlink; lo dejo intacto." >&2
    continue
  fi
  ln -sfn "${skill%/}" "$target"
  echo "✓ skill $name"
done

# --- 2. store ---
# Cada pieza se verifica y repara por separado. En una máquina nueva el store puede
# existir a medias, así que "el directorio existe" NO significa "está instalado".
mkdir -p "$STORE_DIR"/{projects,memories,shared}

if ! git -C "$STORE_DIR" rev-parse --git-dir >/dev/null 2>&1; then
  git -C "$STORE_DIR" init -q -b main
  echo "✓ repo git inicializado"
fi

# --- 3. remoto (opcional) ---
if [ -n "$REMOTE" ]; then
  if current_remote="$(git -C "$STORE_DIR" remote get-url origin 2>/dev/null)"; then
    if [ "$current_remote" != "$REMOTE" ]; then
      echo "⚠  origin ya apunta a $current_remote, no a $REMOTE. Lo dejo intacto." >&2
      echo "   Para cambiarlo: git -C $STORE_DIR remote set-url origin $REMOTE" >&2
    fi
  else
    git -C "$STORE_DIR" remote add origin "$REMOTE"
    echo "✓ origin → $REMOTE"
  fi

  # Máquina nueva contra un servidor que ya tiene memoria: adoptamos su historia.
  # Se hace ANTES de copiar plantillas, para no chocar con archivos sin versionar.
  if git -C "$STORE_DIR" fetch -q origin 2>/dev/null &&
     git -C "$STORE_DIR" rev-parse -q --verify origin/main >/dev/null; then
    if git -C "$STORE_DIR" rev-parse -q --verify HEAD >/dev/null; then
      git -C "$STORE_DIR" branch --set-upstream-to=origin/main main >/dev/null 2>&1 || true
    else
      git -C "$STORE_DIR" checkout -q -B main --track origin/main
      echo "✓ store traído desde origin"
    fi
  else
    echo "ℹ  no pude leer origin/main (¿remoto vacío, o sin acceso?). Sigo con lo local."
  fi
fi

# --- 4. plantillas: copiar solo lo que falte ---
[ -f "$STORE_DIR/shared/SCHEMA.md" ] || {
  cp "$ENGINE_DIR/templates/SCHEMA.md" "$STORE_DIR/shared/SCHEMA.md"
  echo "✓ shared/SCHEMA.md"
}
[ -f "$STORE_DIR/.gitignore" ] || {
  cp "$ENGINE_DIR/templates/gitignore" "$STORE_DIR/.gitignore"
  echo "✓ .gitignore"
}
touch "$STORE_DIR/projects/.gitkeep" "$STORE_DIR/memories/.gitkeep"

# --- 5. commit de lo que haya quedado suelto ---
if [ -n "$(git -C "$STORE_DIR" status --porcelain)" ]; then
  if git -C "$STORE_DIR" rev-parse -q --verify HEAD >/dev/null; then
    msg="chore: alinea el store con las plantillas del engine"
  else
    msg="init: data store"
  fi
  git -C "$STORE_DIR" add -A
  git -C "$STORE_DIR" commit -q -m "$msg"
  echo "✓ commit: $msg"
fi

if [ -f "$STORE_DIR/.stignore" ]; then
  echo "ℹ  quedó un .stignore del viejo modo P2P; ya no se usa, podés borrarlo."
fi

# --- 6. hook opcional: sugerir /save-context en sesiones largas ---
# Solo se instala con MNEMO_HOOK=1, porque toca ~/.claude/settings.json (global).
# El merge es idempotente: si el hook ya está, no duplica nada.
if [ "${MNEMO_HOOK:-}" = "1" ] || [ "${MNEMO_HOOK:-}" = "true" ]; then
  set +e
  out="$(SETTINGS="$SETTINGS" HOOK_CMD="$HOOK_CMD" node - <<'NODE'
const fs = require("fs"), path = require("path");
const p = process.env.SETTINGS, cmd = process.env.HOOK_CMD;
let s = {};
if (fs.existsSync(p)) {
  try { s = JSON.parse(fs.readFileSync(p, "utf8")) || {}; }
  catch { console.error("INVALID"); process.exit(3); }
}
s.hooks = s.hooks || {};
s.hooks.PreToolUse = s.hooks.PreToolUse || [];
const has = s.hooks.PreToolUse.some(e =>
  (e.hooks || []).some(h => typeof h.command === "string" && h.command.includes("suggest-save.js")));
if (has) { console.log("EXISTS"); process.exit(0); }
s.hooks.PreToolUse.push({ matcher: "Edit|Write", hooks: [{ type: "command", command: cmd }] });
fs.mkdirSync(path.dirname(p), { recursive: true });
fs.writeFileSync(p, JSON.stringify(s, null, 2) + "\n");
console.log("ADDED");
NODE
)"
  rc=$?
  set -e
  case "$out" in
    ADDED)  echo "✓ hook instalado en $SETTINGS (sugiere /save-context; reinicia Claude Code)";;
    EXISTS) echo "✓ hook ya estaba en $SETTINGS";;
    *)      echo "⚠  no pude editar $SETTINGS (¿JSON inválido? rc=$rc). Agrégalo a mano:" >&2
            echo "   \"hooks\":{\"PreToolUse\":[{\"matcher\":\"Edit|Write\",\"hooks\":[{\"type\":\"command\",\"command\":\"$HOOK_CMD\"}]}]}" >&2;;
  esac
else
  echo "ℹ  hook de sugerencia de guardado: no instalado. Para activarlo: MNEMO_HOOK=1 ./install.sh"
fi

echo
echo "Engine: $ENGINE_DIR   (compartible en GitHub)"
echo "Store:  $STORE_DIR   (tu data)"
echo "Skills: /list-context /load-context /save-context /mem /forget"
echo
if [ -n "$REMOTE" ]; then
  echo "El store sincroniza contra $REMOTE. Los skills hacen pull --rebase al leer"
  echo "y te piden confirmación antes de cada push."
else
  echo "El store es solo local. Para sincronizarlo entre máquinas, apuntalo a un"
  echo "remoto git tuyo (bare repo en un VPS, repo privado, Gitea...):"
  echo "  git -C $STORE_DIR remote add origin <URL> && git -C $STORE_DIR push -u origin main"
fi
echo
echo "Si tu store NO está en la ruta default, exporta MNEMO_DIR en tu shell rc."
