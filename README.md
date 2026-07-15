# mnemo

Memoria persistente **por proyecto** para Claude Code: guarda el contexto de tu trabajo como
**texto plano** en un repo git y lo mantiene **sincronizado entre tus máquinas**. Simple,
domain-agnostic, tu data es tuya.

Es como [engram](https://github.com/Gentleman-Programming/engram) pero minimalista y sin base de
datos: la memoria son archivos `.md` versionados en git, no un binario opaco.

> **Estado: temprano (WIP).** Los comandos y el hook corren en local como plugin de Claude Code.
> El flujo multi-máquina está diseñado pero poco probado.

## Dos piezas separadas

| | Qué es | Dónde vive |
|---|---|---|
| **Plugin** (este repo) | La herramienta: los slash-commands `/mnemo:*` + el hook | GitHub — **compartible**. Cualquiera lo instala y lo usa para lo suyo |
| **Store** | *Tu* memoria: tus proyectos y notas | Donde vos decidas: un repo git en tu servidor, o solo en tu máquina |

Cada persona instala el mismo plugin y tiene **su propio store privado**. Tu contexto no se mezcla
con el de nadie.

**El plugin no elige tu servidor.** Solo necesita un remoto git; qué hay del otro lado es cosa
tuya: un bare repo en un VPS, un repo privado en GitHub/GitLab, un Gitea self-hosted. O ninguno,
y el store se queda en esta máquina.

## Cómo funciona (en 30 segundos)

- Una **nota** = un `.md` en el store con etiquetas al inicio (`projects: [a, b]`, `type`, ...).
- El overlap es nativo: una nota puede pertenecer a **varios proyectos** a la vez (etiquetas, no
  carpetas rígidas). Cargar un proyecto = filtrar las notas que lo incluyen.
- **`projects` vs `services`:** `projects` dice *de qué proyecto(s)* es la nota (overlap = varios
  proyectos distintos); `services` distingue *partes internas* de un mismo proyecto (repos,
  módulos, áreas). Dos repos de un mismo producto = **un** proyecto con dos `services`, no dos
  proyectos.
- Los comandos hacen el trabajo sucio: `pull --rebase` → leer/escribir notas → `commit` → `push`
  (con tu confirmación). Nunca escribes git a mano.

## Instalación

Es un plugin de Claude Code. Desde Claude Code:

```
/plugin marketplace add AlexParco/mnemo
/plugin install mnemo@mnemo
```

No hay instalador ni paso de setup del store: **el store se crea solo** la primera vez que corres
`/mnemo:save-context <slug>` (git init + estructura, en `~/.local/share/mnemo`). No necesitas saber
git; los comandos lo gestionan por ti.

En una sola máquina esto ya funciona out-of-the-box, sin servidor ni cuenta de nada. Para tener la
misma memoria en varias máquinas, dale un remoto (siguiente sección).

Variables: `MNEMO_DIR` cambia la ruta del store, `MNEMO_REMOTE` engancha un remoto al crearlo.

## Sincronizar entre máquinas

El store es un repo git normal. Apuntalo a un remoto tuyo y las máquinas se sincronizan por ahí.
**Vos elegís el servidor**; al plugin le da igual mientras hable git.

Un bare repo en un VPS propio es lo más barato y lo que menos terceros mete en el medio:

```bash
# en el servidor, una vez
git init --bare ~/mnemo.git
```

Luego, en cada máquina, exporta `MNEMO_REMOTE` **antes** del primer `/mnemo:save-context`:

```bash
export MNEMO_REMOTE=usuario@tu-vps:mnemo.git   # en tu shell rc
```

Al crear el store, mnemo engancha ese remoto. La primera máquina sube su store (`push` con tu
confirmación); las siguientes lo bajan solas: si el remoto ya tiene memoria, se adopta esa historia
en vez de crear una nueva. Si el store ya existía sin remoto, agrégalo a mano una vez:

```bash
git -C ~/.local/share/mnemo remote add origin <URL> && git -C ~/.local/share/mnemo push -u origin main
```

Si preferís un repo privado en GitHub/GitLab o un Gitea self-hosted, cambia la URL y nada más.
Ojo con la privacidad: en un servicio de terceros tu memoria vive en su disco.

### Conflictos

Los resuelve git, no un daemon. Antes de escribir, los comandos hacen `pull --rebase`; si dos
máquinas tocaron la misma nota, se fusiona el `.md` conservando ambos lados. Como cada memoria es
**un hecho por archivo**, los choques son raros; el único archivo caliente es `pending.md`, donde
la fusión correcta casi siempre es la unión de las tareas. Si el conflicto es semántico (dos
decisiones que se contradicen), el comando se para y te pregunta en vez de elegir por vos.

### Backup

El remoto es tu copia de respaldo, además del clon de cada máquina. Si el servidor es tuyo,
respaldalo como respaldás cualquier otra cosa tuya.

## Uso

| Comando | Qué hace |
|---|---|
| `/mnemo:list-context` | panorama del store: proyectos, estado, nº de memorias. Solo lectura |
| `/mnemo:load-context <slug>` | carga un proyecto (INDEX + notas tagueadas + pendientes) y te deja retomar. Slug obligatorio; sin él → lista |
| `/mnemo:save-context <slug>` | destila la sesión en notas tagueadas, actualiza pendientes, commitea y (con tu confirmación) sube. Crea el store y el proyecto si no existen. Slug obligatorio; sin él → lista |
| `/mnemo:mem <slug>[,slug2] <nota>` | guarda una nota suelta a mitad de sesión, commit local |
| `/mnemo:rename <viejo> <nuevo>` | renombra el slug de un proyecto (dir + INDEX + `projects` de cada nota, overlap-safe) |
| `/mnemo:forget project\|memory <x>` | borra un proyecto o una nota (overlap-safe: una nota compartida se desetiqueta, no se borra) |

Ningún comando sube nada sin que lo confirmes. Hasta que subas, tus otras máquinas no lo ven.

### Desde cero

No hay comando "crear proyecto": lo bootstrapeas con `/mnemo:save-context <slug>` la primera vez
(Claude te pide el nombre y arma el `INDEX.md` + `pending.md`; y si el store no existía, lo crea).
Luego, en cualquier laptop, `/mnemo:load-context <slug>` retoma donde quedaste.

### Recordatorio de guardado (hook incluido)

Es fácil trabajar una sesión larga y olvidarte de `/mnemo:save-context`. El plugin trae un hook que,
tras acumular ediciones sin guardar, te sugiere correr `/mnemo:save-context` (no bloquea nada, solo
avisa). Cuenta **trabajo sin persistir**: en cuanto guardas, el contador se reinicia solo y se calla.

Viene activo con el plugin. Se ajusta o apaga por env:

| Variable | Default | Qué hace |
|---|---|---|
| `MNEMO_SAVE_EDITS` | `40` | ediciones sin guardar antes de avisar (y cada cuántas re-avisar). `0` lo apaga |
| `MNEMO_SAVE_TOKENS` | `0` (off) | avisa también si el contexto supera N tokens (señal opcional; el formato del transcript es interno de Claude Code) |
| `MNEMO_SAVE_TOKENS_STEP` | `60000` | re-aviso por tokens |

## Estructura del store

```
<store>/
  projects/<slug>/INDEX.md     # qué es, alcance, estado
  projects/<slug>/pending.md   # tareas pendientes -> "seguir donde quedé"
  memories/<id>.md             # notas atómicas, multi-tag (aquí vive el overlap)
  shared/SCHEMA.md             # contrato del frontmatter
```

Contrato completo del frontmatter: `templates/SCHEMA.md` (se copia al store en el primer guardado).

## Estructura del plugin

```
mnemo/
  .claude-plugin/plugin.json        # manifest (nombre, versión, referencia al hook)
  .claude-plugin/marketplace.json   # catálogo para instalar desde GitHub
  skills/<comando>/SKILL.md         # los 6 slash-commands /mnemo:*
  hooks/hooks.json                  # registra el hook de recordatorio
  scripts/suggest-save.js           # el hook (sin dependencias)
  templates/SCHEMA.md               # contrato del frontmatter (se copia al store)
```

## Futuro

El store son archivos. Si algún día tienes miles de notas y el `grep` se queda corto, se le monta
un índice de búsqueda encima (SQLite/MCP) **sin cambiar tus archivos**. Empezar con texto+git es a
propósito: cero infra, cero lock-in, tu data siempre legible.
