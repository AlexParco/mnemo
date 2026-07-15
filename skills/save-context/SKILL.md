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

1. **Sincroniza primero.** Si hay remoto, `git -C $MEM pull --rebase --autostash` **antes de
   escribir nada**, para no guardar encima de una versión vieja. Si conflictúa, resuélvelo ahora
   (ver "Conflictos" abajo). Si no hay remoto, sigue con lo local.

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
   - Rellena `services`, `type`, `updated` (fecha de hoy) y `author`
     (`git -C $MEM config user.name`).
   - **Antes de crear, busca duplicado** (`grep` por tema/id). Si existe, actualiza ese archivo
     y su `updated` en vez de duplicar.
   - Enlaza memorias relacionadas con `[[id]]`.

5. **Actualiza pendientes** `$MEM/projects/<slug>/pending.md`: marca lo hecho, agrega lo nuevo,
   deja claro qué quedó en curso para el próximo `/load-context`. Actualiza `updated` e info de
   estado en `INDEX.md` si cambió.

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

## Notas

- Muéstrale al usuario un resumen de qué memorias creaste/actualizaste y qué pendientes quedaron.
- Respeta la regla global: nada de commits/push sin que el usuario lo apruebe; invocar
  `/save-context` autoriza el commit local, pero el push se confirma aparte.
