#!/usr/bin/env python3
"""Render a project's strict "resume card" for mnemo.

Usage:  python3 card.py <slug> [lang]
  lang: output language, "en" (default) or "es". Also read from $MNEMO_LANG.
Reads the store from $MNEMO_DIR (or ~/.local/share/mnemo). Deterministic: same
memory + same lang always yields the same card. Exits 1 if the project is missing.

The script (comments, code) is English; only the OUTPUT is localized, so the card
comes out in the language the caller asks for.
"""

import os
import platform
import re
import sys
from pathlib import Path

MAX_PENDING = 5
MAX_ITEM_LEN = 100

# Order and icons per memory type (to group the saved context).
TYPE_ORDER = ["decision", "constraint", "gotcha", "bug", "reference", "todo"]
TYPE_ICON = {
    "decision": "⚖",
    "constraint": "⛔",
    "gotcha": "⚠",
    "bug": "🐛",
    "reference": "🔗",
    "todo": "☐",
}

# Core sections feed the numbered "Pending" list and "Resume with". Bilingual so
# both old (Spanish) and new (English) pending.md files work.
IN_PROGRESS = {"en curso", "in progress"}
NEXT = {"siguiente", "next"}
CORE_SECTIONS = IN_PROGRESS | NEXT

# Non-core pending.md sections render as their own blocks (blocked, debt, deployed,
# etc.). Icons by known name, bilingual; anything else falls back to "▸".
SECTION_ICON = {
    "bloqueado": "⛔", "blocked": "⛔",
    "deuda": "🧾", "debt": "🧾",
    "desplegado": "✅", "deployed": "✅", "hecho": "✅", "done": "✅",
    "ramas": "🌿", "branches": "🌿", "pusheado": "🌿", "pushed": "🌿",
    "riesgos": "⚠", "risks": "⚠",
}

# Localized output labels. The card renders in whichever language the caller asks
# for; the skill passes the language the user is writing in.
LABELS = {
    "en": {
        "services": "Services", "resume": "Resume with", "pending": "Pending",
        "none": "(none)", "more": "more", "saved": "Saved context",
        "no_notes": "(no notes)", "no_summary": "(no summary)",
        "status": {"active": "active", "paused": "paused", "done": "done"},
        "start": "Where do we start?", "one": "Continue with 1?",
        "many": ("Continue with ", " or "),
    },
    "es": {
        "services": "Servicios", "resume": "Retomar por", "pending": "Pendientes",
        "none": "(ninguno)", "more": "más", "saved": "Contexto guardado",
        "no_notes": "(sin notas)", "no_summary": "(sin resumen)",
        "status": {"active": "activo", "paused": "pausado", "done": "hecho"},
        "start": "¿Por dónde arrancamos?", "one": "¿Seguimos con la 1?",
        "many": ("¿Seguimos con ", " o "),
    },
}


def resolve_lang() -> str:
    lang = (sys.argv[2] if len(sys.argv) > 2 else os.environ.get("MNEMO_LANG", "en"))
    lang = lang.strip().lower()[:2]
    return lang if lang in LABELS else "en"


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
    """Simple YAML frontmatter parse: key: value and key: [a, b]."""
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
    """This machine's label: MNEMO_MACHINE, else the short hostname."""
    return (os.environ.get("MNEMO_MACHINE") or platform.node().split(".")[0]).strip()


def machine_flag(text: str, here: str) -> str:
    """'⚠ ' if the item is stamped with a machine other than the current one."""
    m = re.search(r"\[@([^\]]+)\]", text)
    return "⚠ " if m and m.group(1).strip() != here else ""


def parse_pending(path: Path) -> dict:
    """All sections of pending.md, in order. Key = lowercased title; value =
    {'label': original title, 'items': [{'text', 'done'}]}. Generic: works for
    In progress / Next / Blocked as well as Debt / Deployed / anything else."""
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
    """First content line of the body (no frontmatter, no md markers). '' if none."""
    body = text
    if text.startswith("---"):
        end = text.find("\n---", 3)
        if end != -1:
            body = text[end + 4:]
    for line in body.splitlines():
        s = re.sub(r"^[#\-\*>\s]+", "", line.strip()).strip()
        if s:
            return truncate(s)
    return ""


def collect_memories(mem_dir: Path, slug: str) -> list:
    """Memories tagged with the slug, as {type, summary}, sorted by type."""
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
        print("usage: card.py <slug> [lang]", file=sys.stderr)
        return 2
    slug = sys.argv[1]
    L = LABELS[resolve_lang()]
    mem = store_dir()
    proj = mem / "projects" / slug
    if not (proj / "INDEX.md").exists():
        print(f"project '{slug}' not found in {mem}", file=sys.stderr)
        return 1

    fm = parse_frontmatter((proj / "INDEX.md").read_text(encoding="utf-8"))
    name = fm.get("name", slug)
    raw_status = fm.get("status", "?")
    status = L["status"].get(raw_status, raw_status)
    services = fm.get("services", [])
    if not isinstance(services, list):
        services = [services]

    sec = parse_pending(proj / "pending.md")

    def open_texts(keys):
        out = []
        for k in keys:
            out += [i["text"] for i in sec.get(k, {}).get("items", []) if not i["done"]]
        return out

    in_progress = open_texts(IN_PROGRESS)
    nxt = open_texts(NEXT)
    mems = collect_memories(mem / "memories", slug)

    # --- build the card ---
    out = []
    out.append(f"📁 {slug} · {status}")
    out.append(f"   {name}")
    if services:
        shown = ", ".join(services[:2])
        extra = f" (+{len(services) - 2})" if len(services) > 2 else ""
        out.append(f"   {L['services']}: {shown}{extra}")

    resume = in_progress[:2] or nxt[:1]
    out.append("")
    out.append(f"▶ {L['resume']}: {'; '.join(resume) if resume else '—'}")

    here = current_machine()

    pend = in_progress + nxt
    out.append("")
    out.append(L["pending"])
    if pend:
        for i, it in enumerate(pend[:MAX_PENDING], 1):
            out.append(f" {i}. {machine_flag(it, here)}{it}")
        if len(pend) > MAX_PENDING:
            out.append(f"    … (+{len(pend) - MAX_PENDING} {L['more']})")
    else:
        out.append(f" {L['none']}")

    # Extra pending.md sections (blocked, debt, deployed, whatever exists).
    # Shown only if they have items → the card adapts to each project.
    for key, data in sec.items():
        if key in CORE_SECTIONS or not data["items"]:
            continue
        out.append("")
        out.append(f"{section_icon(key)} {data['label']} ({len(data['items'])})")
        for it in data["items"]:
            mark = "✓ " if it["done"] else ""
            out.append(f" • {machine_flag(it['text'], here)}{mark}{it['text']}")

    out.append("")
    out.append(f"{L['saved']} ({len(mems)})")
    if mems:
        for m in mems:
            icon = TYPE_ICON.get(m["type"], "•")
            out.append(f" {icon} {m['summary'] or L['no_summary']}")
    else:
        out.append(f" {L['no_notes']}")

    shown = min(len(pend), MAX_PENDING)
    if shown == 0:
        prompt = L["start"]
    elif shown == 1:
        prompt = L["one"]
    else:
        nums = [str(i) for i in range(1, shown + 1)]
        head, joiner = L["many"]
        prompt = head + ", ".join(nums[:-1]) + joiner + nums[-1] + "?"
    out.append("")
    out.append(prompt)

    print("\n".join(out))
    return 0


if __name__ == "__main__":
    sys.exit(main())
