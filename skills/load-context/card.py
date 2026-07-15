#!/usr/bin/env python3
"""Renderiza la 'tarjeta de retomar' de un proyecto de mnemo, en formato estricto.

Uso:  python3 card.py <slug>
Lee el store de $MNEMO_DIR (o ~/.local/share/mnemo). Determinista: la misma
memoria produce siempre la misma tarjeta. Si el proyecto no existe, sale 1.
"""

import os
import platform
import re
import sys
from pathlib import Path

STATUS_ES = {"active": "activo", "paused": "pausado", "done": "hecho"}
MAX_PENDING = 5
MAX_ITEM_LEN = 100

# Orden e íconos por tipo de memoria (para agrupar el contexto).
TYPE_ORDER = ["decision", "constraint", "gotcha", "bug", "reference", "todo"]
TYPE_ICON = {
    "decision": "⚖",
    "constraint": "⛔",
    "gotcha": "⚠",
    "bug": "🐛",
    "reference": "🔗",
    "todo": "☐",
}

# Secciones de pending.md que NO son "pendientes" numerados: se muestran como
# bloques propios (bloqueado, deuda, desplegado, etc.). Íconos por nombre conocido.
CORE_SECTIONS = {"en curso", "siguiente"}
SECTION_ICON = {
    "bloqueado": "⛔", "deuda": "🧾", "desplegado": "✅", "hecho": "✅",
    "ramas": "🌿", "riesgos": "⚠", "pusheado": "🌿",
}


def section_icon(key: str) -> str:
    for name, icon in SECTION_ICON.items():
        if key.startswith(name):
            return icon
    return "▸"


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


def current_machine() -> str:
    """Etiqueta de esta máquina: MNEMO_MACHINE, si no el hostname corto."""
    return (os.environ.get("MNEMO_MACHINE") or platform.node().split(".")[0]).strip()


def machine_flag(text: str, here: str) -> str:
    """'⚠ ' si el ítem está estampado con una máquina distinta a la actual."""
    m = re.search(r"\[@([^\]]+)\]", text)
    return "⚠ " if m and m.group(1).strip() != here else ""


def parse_pending(path: Path) -> dict:
    """Todas las secciones del pending.md, en orden. Clave = título en minúsculas;
    valor = {'label': título original, 'items': [{'text', 'done'}]}. Genérico: sirve
    para En curso / Siguiente / Bloqueado y también Deuda / Desplegado / lo que haya."""
    sections: dict = {}
    current = None
    if not path.exists():
        return sections
    for line in path.read_text(encoding="utf-8").splitlines():
        h = re.match(r"^##\s+(.*)$", line.strip())
        if h:
            label = h.group(1).strip()
            current = label.lower()
            sections.setdefault(current, {"label": label, "items": []})
            continue
        item = re.match(r"^\s*-\s*\[( |x|X)\]\s*(.+)$", line)
        if item and current is not None:
            sections[current]["items"].append({
                "text": truncate(item.group(2)),
                "done": item.group(1).lower() == "x",
            })
    return sections


def memory_summary(text: str) -> str:
    """Primera línea de contenido del cuerpo (sin frontmatter ni marcadores md)."""
    body = text
    if text.startswith("---"):
        end = text.find("\n---", 3)
        if end != -1:
            body = text[end + 4:]
    for line in body.splitlines():
        s = re.sub(r"^[#\-\*>\s]+", "", line.strip()).strip()
        if s:
            return truncate(s)
    return "(sin resumen)"


def collect_memories(mem_dir: Path, slug: str) -> list:
    """Memorias tagueadas con el slug, como {type, summary}, ordenadas por tipo."""
    items = []
    if not mem_dir.exists():
        return items
    for f in sorted(mem_dir.glob("*.md")):
        text = f.read_text(encoding="utf-8")
        fm = parse_frontmatter(text)
        if slug in project_slugs(fm):
            items.append({"type": fm.get("type", ""), "summary": memory_summary(text)})
    items.sort(key=lambda it: TYPE_ORDER.index(it["type"])
               if it["type"] in TYPE_ORDER else len(TYPE_ORDER))
    return items


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

    def open_texts(key):
        return [i["text"] for i in sec.get(key, {}).get("items", []) if not i["done"]]

    en_curso = open_texts("en curso")
    siguiente = open_texts("siguiente")
    mems = collect_memories(mem / "memories", slug)

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

    here = current_machine()

    pend = en_curso + siguiente
    out.append("")
    out.append("Pendientes")
    if pend:
        for i, it in enumerate(pend[:MAX_PENDING], 1):
            out.append(f" {i}. {machine_flag(it, here)}{it}")
        if len(pend) > MAX_PENDING:
            out.append(f"    … (+{len(pend) - MAX_PENDING} más)")
    else:
        out.append(" (ninguno)")

    # Secciones extra del pending.md (bloqueado, deuda, desplegado, lo que exista).
    # Se muestran solo si tienen items → la tarjeta se adapta a cada proyecto.
    for key, data in sec.items():
        if key in CORE_SECTIONS or not data["items"]:
            continue
        out.append("")
        out.append(f"{section_icon(key)} {data['label']} ({len(data['items'])})")
        for it in data["items"]:
            mark = "✓ " if it["done"] else ""
            out.append(f" • {machine_flag(it['text'], here)}{mark}{it['text']}")

    out.append("")
    out.append(f"Contexto guardado ({len(mems)})")
    if mems:
        for m in mems:
            icon = TYPE_ICON.get(m["type"], "•")
            out.append(f" {icon} {m['summary']}")
    else:
        out.append(" (sin notas)")

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
