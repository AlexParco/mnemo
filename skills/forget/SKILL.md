---
name: forget
description: Borra de la memoria persistente un proyecto entero o una memoria suelta, de forma segura ante overlap (una memoria compartida con otros proyectos se desetiqueta, no se borra). Uso "/mnemo:forget project <slug>" o "/mnemo:forget memory <id>". Trigger cuando el usuario quiere borrar/eliminar un proyecto de la memoria, "borra el proyecto X", "elimina X de la memoria", "olvida X", "borra la memoria Y", o limpiar entradas del store. Agnóstico: cualquier proyecto.
---

# forget

Elimina entradas del store de `mnemo`. Operación **destructiva** → protocolo de
confirmación obligatorio. Nunca borres sin mostrar antes qué se va y sin confirmación explícita.

## Store

`$MEM = $MNEMO_DIR` o `~/.local/share/mnemo`.

**Si `$MEM` no existe o no es repo git:** con `MNEMO_REMOTE` seteado, clónalo del hub
(`git clone "$MNEMO_REMOTE" "$MEM"`) para borrar sobre la memoria compartida real; sin
`MNEMO_REMOTE`, no hay nada que borrar: avisa y detente.

## Sincroniza primero

Si hay remoto, `git -C $MEM pull --rebase --autostash` antes de tocar nada: borrar sobre una copia
vieja puede eliminar una memoria que otra máquina acaba de escribir. Si el pull conflictúa,
resuélvelo (o para y pregunta) **antes** de clasificar nada, y continúa con
`GIT_EDITOR=true git -C $MEM rebase --continue` — sin `GIT_EDITOR` la shell se cuelga en el editor.

## Modo A — borrar un proyecto: `/mnemo:forget project <slug>`

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

5. **Verifica después:** confirma que ningún frontmatter sigue listando `<slug>` como proyecto, y
   que las memorias compartidas siguen existiendo con sus otros proyectos. Para verificar, **lee el
   campo `projects` de las memorias que grepeaste en el paso 2**; no grepees el slug pelado contra
   todo el store: aparece en la prosa de las notas y te dará falsos positivos (y un slug corto como
   `mnemo` hace match dentro de `mnemo-web`). Reporta el resultado.

6. **Commit** `git -C $MEM add -A && git -C $MEM commit -m "forget(project <slug>): <resumen>"`.
   Sin `Co-Authored-By`. **Push solo con confirmación aparte** (muestra qué se subirá). Recuerda
   al usuario que hasta que suba, las otras máquinas conservan lo borrado.

## Modo B — borrar una memoria: `/mnemo:forget memory <id>`

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
