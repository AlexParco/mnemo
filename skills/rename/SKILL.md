---
name: rename
description: Renombra el slug (la identidad) de un proyecto en la memoria persistente, actualizando el directorio, el INDEX y el campo projects de cada memoria tagueada, de forma segura ante overlap. Uso "/mnemo:rename <slug-viejo> <slug-nuevo>". Trigger cuando el usuario quiere renombrar un proyecto, cambiarle el slug o el identificador, "renombra el proyecto X a Y", "cambia el slug de X", "el proyecto se llama mal". Para cambiar solo el nombre legible (no el slug) basta editar el INDEX. Agnóstico: cualquier proyecto.
---

# rename

Cambia el **slug** de un proyecto (su identidad) en todo el store. El slug vive en el nombre del
directorio, en el `INDEX.md`, y en el campo `projects` de cada memoria tagueada — hay que tocar los
tres o el proyecto queda inconsistente. Operación **mutante** → protocolo de confirmación.

> ¿Solo querés cambiar el **nombre legible** (el `name:` que se muestra, no el slug)? Eso es una
> edición de una línea en `INDEX.md`, no necesita este skill. `rename` es para el **slug**.

## Store

`$MEM = $MNEMO_DIR` o `~/.local/share/mnemo`.

**Si `$MEM` no existe o no es repo git:** con `MNEMO_REMOTE` seteado, clónalo del hub
(`git clone "$MNEMO_REMOTE" "$MEM"`) para renombrar sobre la memoria compartida real; sin
`MNEMO_REMOTE`, no hay nada que renombrar: avisa y detente.

## Sincroniza primero

Si hay remoto, `git -C $MEM pull --rebase --autostash` antes de tocar nada, para no renombrar sobre
una copia vieja. Si conflictúa, resuélvelo (o para y pregunta si el choque es semántico) y continúa
con `GIT_EDITOR=true git -C $MEM rebase --continue` — sin `GIT_EDITOR` la shell se cuelga en el editor.

## Pasos

1. **Parsea el argumento:** `<slug-viejo> <slug-nuevo>`. Si falta alguno, pídelo. El slug nuevo
   debe ser kebab-case válido.

2. **Valida:**
   - `$MEM/projects/<slug-viejo>/` **existe**. Si no, lista los proyectos (`ls $MEM/projects`) y
     pregunta a cuál se refería.
   - `$MEM/projects/<slug-nuevo>/` **NO existe**. Si existe, para: renombrar fusionaría dos
     proyectos y este skill no hace merge. Avísale al usuario.

3. **Reúne el impacto y confírmalo.** Encuentra las memorias que taguean el slug viejo:
   `grep -rl "<slug-viejo>" $MEM/memories`. **Lee el campo `projects` de cada una** para quedarte
   solo con las que lo listan como proyecto (no las que lo mencionan en la prosa; un slug corto
   puede aparecer dentro de otro, ej. `mnemo` dentro de `mnemo-web`). **Muestra al usuario** qué se
   va a cambiar (directorio, INDEX, y la lista de memorias afectadas) y **pide confirmación
   explícita** antes de tocar nada.

4. **Ejecuta:**
   - Renombra el directorio: `git -C $MEM mv projects/<slug-viejo> projects/<slug-nuevo>`.
   - En `$MEM/projects/<slug-nuevo>/INDEX.md`, cambia `slug: <slug-viejo>` → `slug: <slug-nuevo>`.
   - En **cada memoria afectada**, cambia el slug **solo dentro del campo `projects:`** del
     frontmatter (respeta el overlap: si es `projects: [<slug-viejo>, otro]`, queda
     `projects: [<slug-nuevo>, otro]`). No toques la prosa del cuerpo.
   - Actualiza `updated` (hoy) en el `INDEX.md` y en las memorias que editaste.

5. **Verifica después:** ningún frontmatter debe listar ya `<slug-viejo>` como proyecto (revisa el
   campo `projects` de las memorias que grepeaste, no el slug pelado contra todo el store),
   `projects/<slug-viejo>/` ya no existe y `projects/<slug-nuevo>/` sí. Reporta el resultado.

6. **Commit** `git -C $MEM add -A && git -C $MEM commit -m "rename(<slug-viejo> → <slug-nuevo>): <resumen>"`.
   Sin `Co-Authored-By`. **Push solo con confirmación aparte** (muestra qué se subirá). Recuérdale
   que hasta que suba, las otras máquinas conservan el slug viejo.

## Notas

- El slug nuevo es la identidad nueva: a partir de aquí `/mnemo:load-context <slug-nuevo>`.
- Si el proyecto tenía `pending.md` con el nombre en el título, actualízalo también si aplica.
