---
name: save-context
description: Guarda en la memoria persistente los avances, decisiones y pendientes del trabajo de la sesión actual, etiquetados por proyecto, y los sincroniza al store git de mnemo. Uso "/mnemo:save-context <proyecto>". Trigger cuando el usuario quiere guardar el contexto, cerrar/pausar una sesión conservando el avance, "guarda lo que hicimos en X", "apunta esto en la memoria de X", o antes de cambiar de proyecto. Agnóstico: sirve para cualquier proyecto.
---

# save-context

Persiste lo aprendido/decidido en la sesión como memorias atómicas tagueadas y actualiza los
pendientes del proyecto, sincronizando al repo git.

## Resolución del store

`$MEM = $MNEMO_DIR` o `~/.local/share/mnemo`.

**Bootstrap (primer uso).** `save-context` es el comando que da de alta el store: si `$MEM` no
existe o no es un repo git, créalo antes de seguir. Corré:

```bash
mkdir -p "$MEM"/{projects,memories,shared}
git -C "$MEM" rev-parse --git-dir >/dev/null 2>&1 || git -C "$MEM" init -q -b main
# Remoto opcional + adopción de historia. Va ANTES de escribir plantillas locales:
# en una máquina nueva contra un VPS que ya tiene memoria, el checkout traería el
# store, y un .gitignore/SCHEMA.md sin versionar lo bloquearía.
if [ -n "${MNEMO_REMOTE:-}" ] && ! git -C "$MEM" remote get-url origin >/dev/null 2>&1; then
  git -C "$MEM" remote add origin "$MNEMO_REMOTE"
  if git -C "$MEM" fetch -q origin 2>/dev/null && git -C "$MEM" rev-parse -q --verify origin/main >/dev/null; then
    git -C "$MEM" rev-parse -q --verify HEAD >/dev/null || git -C "$MEM" checkout -q -B main --track origin/main
  fi
fi
# Plantillas: solo lo que falte (en una máquina nueva ya vinieron del remoto).
[ -f "$MEM/.gitignore" ] || printf '.DS_Store\n' > "$MEM/.gitignore"
# copia el contrato del schema si el plugin lo trae a mano (best-effort)
[ -f "$MEM/shared/SCHEMA.md" ] || cp "${CLAUDE_PLUGIN_ROOT:-/nonexistent}/templates/SCHEMA.md" "$MEM/shared/SCHEMA.md" 2>/dev/null || true
```

Si el bootstrap acaba de crear el store, avísale al usuario en una línea dónde quedó (`$MEM`) y
que para sincronizar entre máquinas puede exportar `MNEMO_REMOTE` o agregar un remoto git a mano.

## Pasos

0. **El slug es obligatorio.** Si el usuario no pasó proyecto, **no guardes nada**: ejecuta el
   comportamiento de `/mnemo:list-context` (muestra los proyectos existentes) y pídele a cuál guardar,
   o que confirme un slug nuevo. Nunca guardes "por defecto" ni adivines el proyecto.

1. **Sincroniza primero.** Si hay remoto, `git -C $MEM pull --rebase --autostash` **antes de
   escribir nada**, para no guardar encima de una versión vieja. Si conflictúa, resuélvelo ahora
   (ver "Conflictos" abajo). Si no hay remoto, sigue con lo local.

2. **Resuelve el proyecto** (slug argumento). Si `$MEM/projects/<slug>/` no existe, ofrece
   crearlo: pide nombre legible y crea `INDEX.md` (con frontmatter del SCHEMA) y `pending.md`
   vacío. No crees un proyecto en silencio.

3. **Destila la sesión.** Revisa lo trabajado y extrae SOLO lo que valga persistir:
   decisiones tomadas, restricciones/invariantes descubiertas, gotchas, bugs conocidos,
   referencias útiles. **No** guardes lo que ya está en el código, en git, o es efímero de esta
   conversación. Ante la duda, menos es más. **Si no hay nada que valga la pena, díselo al usuario
   y no crees notas vacías ni triviales solo para "guardar algo".**

4. **Escribe memorias atómicas** en `$MEM/memories/<id>.md` siguiendo `shared/SCHEMA.md`:
   - Un hecho por archivo. `id` = nombre de archivo, kebab-case único.
   - `projects: [<slug>, ...]` — normalmente solo el slug actual. **Overlap** = añadir OTRO
     proyecto solo si el mismo hecho también le sirve a un proyecto **distinto** (ej. un bug de una
     librería compartida). No confundas overlap con partes internas del mismo proyecto: para
     distinguir repos/módulos/áreas de este proyecto usá `services`, **no** `projects`. Pregunta al
     usuario antes de taguear un segundo proyecto si no está claro.
   - Rellena `services` (qué repo/módulo/área toca dentro del proyecto), `type`, `updated` (fecha
     de hoy) y `author` (`git -C $MEM config user.name`).
   - **Antes de crear, busca duplicado** (`grep` por tema/id). Si existe, actualiza ese archivo
     y su `updated` en vez de duplicar.
   - Enlaza memorias relacionadas con `[[id]]`.

5. **Actualiza pendientes** `$MEM/projects/<slug>/pending.md`: marca lo hecho, agrega lo nuevo,
   deja claro qué quedó en curso para el próximo `/mnemo:load-context`. Las secciones son **libres**
   y propias de cada proyecto (`load-context` muestra las que existan): además de
   `En curso`/`Siguiente`/`Bloqueado`, agregá las que la sesión haya producido y ayuden a retomar —
   p. ej. `## Deuda`, `## Ramas` (qué se pusheó a qué rama), `## Hecho`/`Desplegado` (`- [x]`, para
   no repisar), `## Riesgos`. **Solo las que apliquen a ESTE proyecto**; no fuerces secciones vacías
   ni copies las de otro. Actualiza `updated` e info de estado en `INDEX.md` si cambió.

6. **Commit.** `git -C $MEM add -A && git -C $MEM commit -m "save(<slug>): <resumen corto>"`.
   Nunca uses `Co-Authored-By`.

7. **Push (con confirmación).** Si hay remoto, **muestra al usuario** qué se va a subir
   (`git -C $MEM log origin/main..HEAD --oneline` y `git -C $MEM show --stat HEAD`) y
   **pide confirmación explícita antes de `git push`**. Sin confirmación, deja el commit local
   y avísale que quedó sin subir — hasta que suba, las otras máquinas no lo ven.

## Conflictos

Con un remoto compartido, dos máquinas pueden tocar la misma nota. El store son `.md`, así que
el rebase se resuelve leyendo, no adivinando:

- **Fusiona, no descartes.** Ante marcadores de conflicto, el default es **conservar la
  información de ambos lados**. Perder una memoria es peor que dejar una nota redundante.
- **`pending.md` es el único archivo caliente** (las memorias son un hecho por archivo, casi nunca
  chocan). La fusión correcta es casi siempre la **unión** de las tareas de ambos lados; quita
  duplicados y respeta lo que ya esté marcado como hecho en cualquiera de los dos.
- **Si el choque es semántico —los dos lados afirman cosas contradictorias— para y pregunta.**
  No elijas tú qué decisión sobrevive.
- Resuelto el archivo: `git -C $MEM add <archivo>` y luego
  **`GIT_EDITOR=true git -C $MEM rebase --continue`**. El `GIT_EDITOR=true` no es opcional: sin él
  git abre un editor para el mensaje de commit y tu shell se queda colgada esperando. (`--continue`
  tampoco acepta `-q`.)
- Si te enredas, `git -C $MEM rebase --abort` deja todo como estaba y avisas al usuario.
- Antes de dar por cerrado: `git -C $MEM status` no debe mostrar rebase en curso, y no puede quedar
  ningún `<<<<<<<` en los `.md` (`grep -rn '^<<<<<<<' $MEM`).

## Formato de salida

Confirmación **compacta**, no un volcado de lo que escribiste. Formato:

```
✅ Guardado en <slug>
 • Memorias: <N> nueva(s)/actualizada(s) — <ids o temas cortos, coma-separados>
 • Pendientes: <qué cambió, ej. "+2 nuevos, 1 marcado hecho">
 • Commit <hash corto> · <"subido al hub ✓" | "local, sin subir (confirmá para push)">
```

No repitas el contenido de cada memoria; el usuario ya vivió la sesión. Si no hubo nada que
guardar, una línea: "Nada nuevo que persistir en <slug>."

## Notas

- Respeta la regla global: nada de commits/push sin que el usuario lo apruebe; invocar
  `/mnemo:save-context` autoriza el commit local, pero el push se confirma aparte.
