---
name: save-context
description: Guarda en la memoria persistente los avances, decisiones y pendientes del trabajo de la sesión actual, etiquetados por proyecto, y los sincroniza al store git de mnemo. Uso "/save-context <proyecto>". Trigger cuando el usuario quiere guardar el contexto, cerrar/pausar una sesión conservando el avance, "guarda lo que hicimos en X", "apunta esto en la memoria de X", o antes de cambiar de proyecto. Agnóstico: cualquier proyecto, no solo Ari.
---

# save-context

Persiste lo aprendido/decidido en la sesión como memorias atómicas tagueadas y actualiza los
pendientes del proyecto, sincronizando al repo git.

## Resolución del store

Igual que load-context: `$MEM = $MNEMO_DIR` o `~/.local/share/mnemo`. Si no existe, avisa y
detente.

## Pasos

0. **El slug es obligatorio.** Si el usuario no pasó proyecto, **no guardes nada**: ejecuta el
   comportamiento de `/list-context` (muestra los proyectos existentes) y pídele a cuál guardar,
   o que confirme un slug nuevo. Nunca guardes "por defecto" ni adivines el proyecto.

1. **Sincroniza primero.** En P2P, Syncthing ya trajo los cambios de tus otras máquinas; si además
   hay remoto git (modo VPS opcional), `git -C $MEM pull --ff-only` para no divergir.
   **Antes de escribir, chequea conflictos:** `find $MEM -name '*.sync-conflict-*'`. Si hay alguno,
   páralo y avísale al usuario para resolverlo primero — no quieres guardar encima de una versión
   sin fusionar.

2. **Resuelve el proyecto** (slug argumento). Si `$MEM/projects/<slug>/` no existe, ofrece
   crearlo: pide nombre legible y crea `INDEX.md` (con frontmatter del SCHEMA) y `pending.md`
   vacío. No crees un proyecto en silencio.

3. **Destila la sesión.** Revisa lo trabajado y extrae SOLO lo que valga persistir:
   decisiones tomadas, restricciones/invariantes descubiertas, gotchas, bugs conocidos,
   referencias útiles. **No** guardes lo que ya está en el código, en git, o es efímero de esta
   conversación. Ante la duda, menos es más.

4. **Escribe memorias atómicas** en `$MEM/memories/<id>.md` siguiendo `shared/SCHEMA.md`:
   - Un hecho por archivo. `id` = nombre de archivo, kebab-case único.
   - `projects: [<slug>, ...]` — incluye el slug actual; **si el hecho también aplica a otro
     proyecto que toca el mismo servicio, taguéalo con ambos** (overlap explícito). Pregunta al
     usuario si no está claro que aplique a más de un proyecto.
   - Rellena `services`, `type`, `author`, `updated` (fecha de hoy).
   - **Antes de crear, busca duplicado** (`grep` por tema/id). Si existe, actualiza ese archivo
     y su `updated` en vez de duplicar.
   - Enlaza memorias relacionadas con `[[id]]`.

5. **Actualiza pendientes** `$MEM/projects/<slug>/pending.md`: marca lo hecho, agrega lo nuevo,
   deja claro qué quedó en curso para el próximo `/load-context`. Actualiza `updated` e info de
   estado en `INDEX.md` si cambió.

6. **Commit.** `git -C $MEM add -A && git -C $MEM commit -m "save(<slug>): <resumen corto>"`.
   Nunca uses `Co-Authored-By`.

7. **Push (con confirmación).** Si hay remoto, **muestra al usuario** qué se va a subir
   (`git -C $MEM log origin/<branch>..HEAD --oneline` y `git -C $MEM show --stat HEAD`) y
   **pide confirmación explícita antes de `git push`**. Sin confirmación, deja el commit local
   y avísale que quedó sin subir.

## Notas

- Muéstrale al usuario un resumen de qué memorias creaste/actualizaste y qué pendientes quedaron.
- Respeta la regla global: nada de commits/push sin que el usuario lo apruebe; invocar
  `/save-context` autoriza el commit local, pero el push se confirma aparte.
