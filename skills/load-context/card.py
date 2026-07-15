#!/usr/bin/env python3
"""Renderiza la 'tarjeta de retomar' de un proyecto de mnemo, en formato estricto.

Uso:  python3 card.py <slug>
Lee el store de $MNEMO_DIR (o ~/.local/share/mnemo). Determinista: la misma
memoria produce siempre la misma tarjeta. Si el proyecto no existe, sale 1.
"""

import os
import re
import sys
from pathlib import Path

STATUS_ES = {"active": "activo", "paused": "pausado", "done": "hecho"}
MAX_PENDING = 5
MAX_ITEM_LEN = 90


def store_dir() -> Path:
    base = os.environ.get("MNEMO_DIR") or os.path.join(
        os.environ.get("XDG_DATA_HOME", os.path.expanduser("~/.local/share")),
        "mnemo",
    )
    return Path(base)


def parse_frontmatter(text: str) -> dict:
    """Parseo simple de frontmatter YAML: key: value y key: [a, b]."""
    fm: dict = {}
    if not text.startswith("---"):
        return fm
    end = text.find("\n---", 3)
    if end == -1:
        return fm
    for line in text[3:end].splitlines():
        m = re.match(r"^([A-Za-z_][\w-]*):\s*(.*)$", line.strip())
        if not m:
            continue
        key, val = m.group(1), m.group(2).strip()
        list_m = re.match(r"^\[(.*)\]$", val)
        if list_m:
            items = [x.strip().strip("'\"") for x in list_m.group(1).split(",")]
            fm[key] = [x for x in items if x]
        else:
            fm[key] = val.strip("'\"")
    return fm


def project_slugs(fm: dict) -> list:
    p = fm.get("projects", [])
    return p if isinstance(p, list) else [p]


def truncate(s: str) -> str:
    s = s.strip()
    return s if len(s) <= MAX_ITEM_LEN else s[: MAX_ITEM_LEN - 1] + "…"


def parse_pending(path: Path) -> dict:
    """Devuelve {'en curso': [...], 'siguiente': [...], 'bloqueado': [...]} con
    solo los items abiertos ('- [ ]')."""
    sections: dict = {}
    current = None
    if not path.exists():
        return sections
    for line in path.read_text(encoding="utf-8").splitlines():
        h = re.match(r"^##\s+(.*)$", line.strip())
        if h:
            current = h.group(1).strip().lower()
            sections.setdefault(current, [])
            continue
        item = re.match(r"^\s*-\s*\[( |x|X)\]\s*(.+)$", line)
        if item and current is not None:
            if item.group(1) == " ":  # solo abiertos
                sections[current].append(truncate(item.group(2)))
    return sections


def count_memories(mem_dir: Path, slug: str) -> int:
    if not mem_dir.exists():
        return 0
    n = 0
    for f in mem_dir.glob("*.md"):
        fm = parse_frontmatter(f.read_text(encoding="utf-8"))
        if slug in project_slugs(fm):
            n += 1
    return n


def main() -> int:
    if len(sys.argv) < 2:
        print("uso: card.py <slug>", file=sys.stderr)
        return 2
    slug = sys.argv[1]
    mem = store_dir()
    proj = mem / "projects" / slug
    if not (proj / "INDEX.md").exists():
        print(f"proyecto '{slug}' no existe en {mem}", file=sys.stderr)
        return 1

    fm = parse_frontmatter((proj / "INDEX.md").read_text(encoding="utf-8"))
    name = fm.get("name", slug)
    status = STATUS_ES.get(fm.get("status", ""), fm.get("status", "?"))
    services = fm.get("services", [])
    if not isinstance(services, list):
        services = [services]

    sec = parse_pending(proj / "pending.md")
    en_curso = sec.get("en curso", [])
    siguiente = sec.get("siguiente", [])
    bloqueado = sec.get("bloqueado", [])
    n_mem = count_memories(mem / "memories", slug)

    # --- armar la tarjeta ---
    out = []
    out.append(f"📁 {slug} · {status}")
    out.append(f"   {name}")
    if services:
        shown = ", ".join(services[:2])
        extra = f" (+{len(services) - 2})" if len(services) > 2 else ""
        out.append(f"   Servicios: {shown}{extra}")

    retomar = en_curso[:2] or siguiente[:1]
    out.append("")
    out.append(f"▶ Retomar por: {'; '.join(retomar) if retomar else '—'}")

    pend = en_curso + siguiente
    out.append("")
    out.append("Pendientes")
    if pend:
        for i, it in enumerate(pend[:MAX_PENDING], 1):
            out.append(f" {i}. {it}")
        if len(pend) > MAX_PENDING:
            out.append(f"    … (+{len(pend) - MAX_PENDING} más)")
    else:
        out.append(" (ninguno)")

    tail = f"🗄 {n_mem} memoria{'s' if n_mem != 1 else ''}"
    if bloqueado:
        tail += f" · {len(bloqueado)} bloqueo{'s' if len(bloqueado) != 1 else ''}"
    out.append("")
    out.append(tail)

    shown = min(len(pend), MAX_PENDING)
    if shown == 0:
        prompt = "¿Por dónde arrancamos?"
    elif shown == 1:
        prompt = "¿Seguimos con la 1?"
    else:
        nums = [str(i) for i in range(1, shown + 1)]
        prompt = "¿Seguimos con " + ", ".join(nums[:-1]) + " o " + nums[-1] + "?"
    out.append("")
    out.append(prompt)

    print("\n".join(out))
    return 0


if __name__ == "__main__":
    sys.exit(main())
