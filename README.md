# mnemo

Memoria persistente **por proyecto** para Claude Code (o cualquier agente): guarda el contexto de
tu trabajo como **texto plano** y lo mantiene **sincronizado P2P entre tus máquinas** (sin servidor).
Simple, domain-agnostic, tu data es tuya.

Es como [engram](https://github.com/Gentleman-Programming/engram) pero minimalista y sin base de
datos: la memoria son archivos `.md` versionados en git, no un binario opaco.

## Dos piezas separadas

| | Qué es | Dónde vive |
|---|---|---|
| **Engine** (este repo) | La herramienta: los slash-commands + el instalador | GitHub — **compartible**. Cualquiera lo clona y lo usa para lo suyo |
| **Store** | *Tu* memoria: tus proyectos y notas | Tus máquinas, sincronizadas **P2P** entre ellas (Syncthing). Nunca toca GitHub ni un servidor |

Cada persona instala el mismo engine y tiene **su propio store privado**. Tu contexto no se mezcla
con el de nadie.

## Cómo funciona (en 30 segundos)

- Una **nota** = un `.md` en el store con etiquetas al inicio (`projects: [a, b]`, `type`, ...).
- El overlap es nativo: una nota puede pertenecer a **varios proyectos** a la vez (etiquetas, no
  carpetas rígidas). Cargar un proyecto = filtrar las notas que lo incluyen.
- Los comandos hacen el trabajo sucio: `git pull` → leer/escribir notas → `git commit`/`push`.
  Nunca escribes git a mano.

## Instalación

```bash
git clone <URL_DEL_ENGINE> ~/mnemo
cd ~/mnemo && ./install.sh
```

`install.sh` hace dos cosas:
1. Enlaza los skills en `~/.claude/skills` (quedan disponibles como `/load-context`, etc.).
2. Crea tu **store** en `~/.local/share/mnemo`: una carpeta con tus notas que además
   es un repo git **local** (te da historial y undo). Lo crea el instalador — **no necesitas
   saber git; los comandos lo gestionan por ti**. Cámbialo con `MNEMO_DIR` si lo quieres
   en otra ruta.

Reinicia Claude Code para que registre los skills.

En una sola máquina esto ya funciona out-of-the-box (sin servidor, sin cuenta de nada). Para tener
la misma memoria en varias máquinas, sincronízalas P2P (siguiente sección).

## Sincronizar entre máquinas (P2P, sin servidor)

Tu memoria se sincroniza **directo entre tus máquinas** con [Syncthing](https://syncthing.net):
cifrado punta a punta, sin VPS, sin GitHub, sin que ningún tercero almacene tu data. No hay que
tocar git: el store simplemente **no lleva remoto** y Syncthing mueve los archivos por debajo.

```bash
# en cada máquina
brew install syncthing        # macOS  (Linux: apt/pacman install syncthing)
syncthing                     # arranca el daemon; abre http://127.0.0.1:8384
```

En la UI de Syncthing (una vez):
1. **Máquina A** → *Add Folder* → apunta a tu store (`~/.local/share/mnemo`).
2. Empareja las dos máquinas con *Add Remote Device* (se intercambian un Device ID).
3. Comparte el folder con la otra máquina; **Máquina B** lo acepta y elige la misma ruta.

Listo: editas la memoria en cualquiera y se sincroniza sola cuando ambas coinciden online.

### Reglas de oro del modo P2P

- **Syncthing NO sincroniza `.git`.** El `.stignore` que instala `install.sh` ya lo excluye. Cada
  máquina lleva su **propio historial git local**; sincronizar las tripas de git a nivel de bytes
  las corrompe. Syncthing mueve solo tus `.md`; los skills committean el historial en cada máquina.
- **Conflictos.** Si editas la *misma* nota en dos máquinas antes de que sincronicen, Syncthing no
  pierde nada: crea una copia `nota.sync-conflict-…md` para que la revises y fusiones a mano. Como
  cada memoria es **un hecho por archivo**, esto es raro; el único archivo caliente es `pending.md`.
  Los skills de lectura te avisan si detectan archivos `*.sync-conflict-*` pendientes.
- **Backup.** Con solo dos máquinas, esas son tus únicas copias. Si te importa el respaldo, añade
  Syncthing a un tercer dispositivo (NAS, Raspberry, otra máquina) como copia pasiva.

> Si preferís un punto siempre encendido (sincronizar aunque la otra máquina esté apagada) en
> lugar de P2P, también podés usar un bare repo git en un VPS como remoto del store. P2P es el
> default por ser cero-infra y cero-costo.

## Uso

| Comando | Qué hace |
|---|---|
| `/list-context` | panorama del store: proyectos, estado, nº de memorias. Solo lectura |
| `/load-context <slug>` | carga un proyecto (INDEX + notas tagueadas + pendientes) y te deja retomar. Slug obligatorio; sin él → lista |
| `/save-context <slug>` | destila la sesión en notas tagueadas, actualiza pendientes, commit local (Syncthing sincroniza solo). Crea el proyecto si no existe. Slug obligatorio; sin él → lista |
| `/mem <slug>[,slug2] <nota>` | guarda una nota suelta a mitad de sesión, commit local |
| `/forget project\|memory <x>` | borra un proyecto o una nota (overlap-safe: una nota compartida se desetiqueta, no se borra) |

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
  .stignore                    # qué NO sincroniza Syncthing (excluye .git)
```

Contrato completo del frontmatter: `templates/SCHEMA.md` (se copia al store al instalar).

## Futuro

El store son archivos. Si algún día tienes miles de notas y el `grep` se queda corto, se le monta
un índice de búsqueda encima (SQLite/MCP) **sin cambiar tus archivos**. Empezar con texto+git es a
propósito: cero infra, cero lock-in, tu data siempre legible.
