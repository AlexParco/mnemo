# mnemo

Memoria persistente **por proyecto** para Claude Code (o cualquier agente): guarda el contexto de
tu trabajo como **texto plano** en un repo git y lo mantiene **sincronizado entre tus máquinas**.
Simple, domain-agnostic, tu data es tuya.

Es como [engram](https://github.com/Gentleman-Programming/engram) pero minimalista y sin base de
datos: la memoria son archivos `.md` versionados en git, no un binario opaco.

> **Estado: temprano (WIP).** Los 5 comandos y el instalador corren en local. El engine todavía
> **no está publicado** en GitHub y el flujo multi-máquina está diseñado pero poco probado.

## Dos piezas separadas

| | Qué es | Dónde vive |
|---|---|---|
| **Engine** (este repo) | La herramienta: los slash-commands + el instalador | GitHub — **compartible**. Cualquiera lo clona y lo usa para lo suyo |
| **Store** | *Tu* memoria: tus proyectos y notas | Donde vos decidas: un repo git en tu servidor, o solo en tu máquina |

Cada persona instala el mismo engine y tiene **su propio store privado**. Tu contexto no se mezcla
con el de nadie.

**El engine no elige tu servidor.** Solo necesita un remoto git; qué hay del otro lado es cosa
tuya: un bare repo en un VPS, un repo privado en GitHub/GitLab, un Gitea self-hosted. O ninguno,
y el store se queda en esta máquina.

## Cómo funciona (en 30 segundos)

- Una **nota** = un `.md` en el store con etiquetas al inicio (`projects: [a, b]`, `type`, ...).
- El overlap es nativo: una nota puede pertenecer a **varios proyectos** a la vez (etiquetas, no
  carpetas rígidas). Cargar un proyecto = filtrar las notas que lo incluyen.
- Los comandos hacen el trabajo sucio: `pull --rebase` → leer/escribir notas → `commit` → `push`
  (con tu confirmación). Nunca escribes git a mano.

## Instalación

Aún no está publicado, así que por ahora se instala desde tu clon local (cuando lo subas a GitHub,
`git clone <URL>` funcionará igual):

```bash
cd ~/mnemo && ./install.sh
```

`install.sh` hace dos cosas:
1. Enlaza los skills en `~/.claude/skills` (quedan disponibles como `/load-context`, etc.).
2. Crea tu **store** en `~/.local/share/mnemo`: una carpeta con tus notas que además es un repo
   git (te da historial y undo). Lo crea el instalador — **no necesitas saber git; los comandos lo
   gestionan por ti**.

Es idempotente: correlo las veces que quieras, y repara un store a medias sin pisar lo que ya hay.

Reinicia Claude Code para que registre los skills.

En una sola máquina esto ya funciona out-of-the-box, sin servidor ni cuenta de nada. Para tener la
misma memoria en varias máquinas, dale un remoto (siguiente sección).

Variables: `MNEMO_DIR` cambia la ruta del store, `MNEMO_REMOTE` configura el remoto.

## Sincronizar entre máquinas

El store es un repo git normal. Apuntalo a un remoto tuyo y las máquinas se sincronizan por ahí.
**Vos elegís el servidor**; al engine le da igual mientras hable git.

Un bare repo en un VPS propio es lo más barato y lo que menos terceros mete en el medio:

```bash
# en el servidor, una vez
git init --bare ~/mnemo.git
```

```bash
# en cada máquina, al instalar
MNEMO_REMOTE=usuario@tu-vps:mnemo.git ./install.sh
```

La primera máquina sube su store (`git push -u origin main`); las siguientes lo bajan solas: si el
remoto ya tiene memoria, `install.sh` adopta esa historia en vez de crear una nueva.

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
| `/list-context` | panorama del store: proyectos, estado, nº de memorias. Solo lectura |
| `/load-context <slug>` | carga un proyecto (INDEX + notas tagueadas + pendientes) y te deja retomar. Slug obligatorio; sin él → lista |
| `/save-context <slug>` | destila la sesión en notas tagueadas, actualiza pendientes, commitea y (con tu confirmación) sube. Crea el proyecto si no existe. Slug obligatorio; sin él → lista |
| `/mem <slug>[,slug2] <nota>` | guarda una nota suelta a mitad de sesión, commit local |
| `/forget project\|memory <x>` | borra un proyecto o una nota (overlap-safe: una nota compartida se desetiqueta, no se borra) |

Ningún comando sube nada sin que lo confirmes. Hasta que subas, tus otras máquinas no lo ven.

### Desde cero

No hay comando "crear proyecto": lo bootstrapeas con `/save-context <slug>` la primera vez
(Claude te pide el nombre y arma el `INDEX.md` + `pending.md`). Luego, en cualquier laptop,
`/load-context <slug>` retoma donde quedaste.

## Estructura del store

```
<store>/
  projects/<slug>/INDEX.md     # qué es, alcance, estado
  projects/<slug>/pending.md   # tareas pendientes -> "seguir donde quedé"
  memories/<id>.md             # notas atómicas, multi-tag (aquí vive el overlap)
  shared/SCHEMA.md             # contrato del frontmatter
```

Contrato completo del frontmatter: `templates/SCHEMA.md` (se copia al store al instalar).

## Futuro

El store son archivos. Si algún día tienes miles de notas y el `grep` se queda corto, se le monta
un índice de búsqueda encima (SQLite/MCP) **sin cambiar tus archivos**. Empezar con texto+git es a
propósito: cero infra, cero lock-in, tu data siempre legible.
