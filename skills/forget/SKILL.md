---
name: forget
description: Borra de la memoria persistente un proyecto entero o una memoria suelta, de forma segura ante overlap (una memoria compartida con otros proyectos se desetiqueta, no se borra). Uso "/forget project <slug>" o "/forget memory <id>". Trigger cuando el usuario quiere borrar/eliminar un proyecto de la memoria, "borra el proyecto X", "elimina X de la memoria", "olvida X", "borra la memoria Y", o limpiar entradas del store. Agnóstico: cualquier proyecto.
---

# forget

Elimina entradas del store de `mnemo`. Operación **destructiva** → protocolo de
confirmación obligatorio. Nunca borres sin mostrar antes qué se va y sin confirmación explícita.

## Store

`$MEM = $MNEMO_DIR` o `~/.local/share/mnemo`. Si no existe, avisa y detente.

## Sincroniza primero

Si hay remoto, `git -C $MEM pull --ff-only` antes de tocar nada, para no borrar sobre una copia
vieja.

## Modo A — borrar un proyecto: `/forget project <slug>`

1. **Verifica** que `$MEM/projects/<slug>/` existe. Si no, avísalo y lista los proyectos que sí
   existen. No asumas.

2. **Clasifica el impacto** ANTES de borrar. Recorre `$MEM/memories/` y separa las memorias cuyo
   frontmatter `projects` incluya `<slug>` en dos grupos:
   - **Exclusivas** (`projects` == `[<slug>]`, solo ese) → se BORRARÁN.
   - **Compartidas** (`projects` tiene `<slug>` + otros) → se DESETIQUETARÁN (se quita `<slug>`
     de la lista, la memoria sobrevive para los demás proyectos).

3. **Muestra el plan exacto** al usuario y pide confirmación:
   - `projects/<slug>/` (INDEX.md, pending.md) → se elimina.
   - Lista de memorias exclusivas a borrar (por `id`).
   - Lista de memorias compartidas a desetiquetar, indicando con qué otros proyectos se quedan.
   - **Espera "sí" explícito.** Sin confirmación, no borres nada.

4. **Ejecuta:**
   - Quita `<slug>` del `projects` de cada memoria compartida (deja el resto intacto) y actualiza
     su `updated`.
   - Borra los archivos de las memorias exclusivas.
   - Borra el directorio `projects/<slug>/`.

5. **Verifica después:** confirma que ya no queda `<slug>` en ningún frontmatter
   (`grep -rn "<slug>" $MEM/memories $MEM/projects` no debe devolver el slug como proyecto) y que
   las memorias compartidas siguen existiendo con sus otros proyectos. Reporta el resultado.

6. **Commit** `git -C $MEM add -A && git -C $MEM commit -m "forget(project <slug>): <resumen>"`.
   Sin `Co-Authored-By`. **Push solo con confirmación aparte** (muestra qué se subirá).

## Modo B — borrar una memoria: `/forget memory <id>`

1. Verifica que `$MEM/memories/<id>.md` existe. Si no, avísalo (ofrece buscar por tema).
2. Muestra su contenido y con qué proyectos está tagueada; pide confirmación explícita.
3. Al confirmar, borra el archivo. Si algún `pending.md` o memoria la enlazaba con `[[id]]`,
   avisa de esos enlaces rotos (no los arregles en silencio).
4. Verifica que ya no existe. Commit con mensaje `forget(memory <id>)`. Push solo confirmado.

## Reglas

- Ante ambigüedad (slug no existe, id dudoso, "borra X" sin decir si es proyecto o memoria),
  **pregunta**; no adivines qué borrar.
- Una memoria compartida JAMÁS se borra al borrar uno solo de sus proyectos: se desetiqueta.
- Respeta la regla global: nada de push sin confirmación explícita del usuario.
