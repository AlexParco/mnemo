#!/usr/bin/env bash
# Instala el ENGINE de mnemo:
#   1) enlaza los skills en ~/.claude/skills
#   2) crea (si no existe) tu DATA STORE en un repo git aparte, listo para sincronizar P2P
# El engine (esto) se comparte en GitHub; el store (tu data) es tuyo y se sincroniza
# directo entre tus máquinas con Syncthing (P2P). Nunca toca GitHub ni un servidor.
# Idempotente.
set -euo pipefail

ENGINE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SKILLS_SRC="$ENGINE_DIR/skills"
SKILLS_DST="$HOME/.claude/skills"
STORE_DIR="${MNEMO_DIR:-${XDG_DATA_HOME:-$HOME/.local/share}/mnemo}"

# --- 1. enlazar skills ---
mkdir -p "$SKILLS_DST"
for skill in "$SKILLS_SRC"/*/; do
  name="$(basename "$skill")"
  target="$SKILLS_DST/$name"
  if [ -e "$target" ] && [ ! -L "$target" ]; then
    echo "⚠  $target existe y NO es symlink; lo dejo intacto." >&2
    continue
  fi
  ln -sfn "$skill" "$target"
  echo "✓ skill $name"
done

# --- 2. crear el data store (separado del engine) ---
if [ ! -d "$STORE_DIR" ]; then
  mkdir -p "$STORE_DIR"/{projects,memories,shared}
  cp "$ENGINE_DIR/templates/SCHEMA.md" "$STORE_DIR/shared/SCHEMA.md"
  cp "$ENGINE_DIR/templates/gitignore" "$STORE_DIR/.gitignore"
  cp "$ENGINE_DIR/templates/stignore" "$STORE_DIR/.stignore"
  touch "$STORE_DIR/projects/.gitkeep" "$STORE_DIR/memories/.gitkeep"
  git -C "$STORE_DIR" init -q -b main
  git -C "$STORE_DIR" add -A
  git -C "$STORE_DIR" commit -q -m "init: data store"
  echo "✓ store creado en $STORE_DIR (repo git local, sin remoto — el historial es tuyo)"
  echo
  echo "Para sincronizar entre máquinas (P2P, sin servidor), comparte esta carpeta con Syncthing:"
  echo "  1. Instala Syncthing en cada máquina (brew install syncthing / apt install syncthing)."
  echo "  2. Añade $STORE_DIR como folder compartido y emparéjalo con tu otra máquina."
  echo "  El .stignore ya excluye .git para no corromper el historial. Ver README."
else
  echo "✓ store ya existe en $STORE_DIR"
  # Backfill del .stignore para stores creados antes del modo P2P.
  if [ ! -f "$STORE_DIR/.stignore" ]; then
    cp "$ENGINE_DIR/templates/stignore" "$STORE_DIR/.stignore"
    echo "✓ .stignore añadido al store (necesario para sincronizar P2P con Syncthing)"
  fi
fi

echo
echo "Engine: $ENGINE_DIR   (compartible en GitHub)"
echo "Store:  $STORE_DIR   (tu data, se sincroniza P2P entre tus máquinas)"
echo "Skills: /list-context /load-context /save-context /mem /forget"
echo
echo "Si tu store NO está en la ruta default, exporta MNEMO_DIR en tu shell rc."
