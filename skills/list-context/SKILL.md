---
name: list-context
description: Muestra el panorama del store de memoria — todos los proyectos con su estado, servicios y cuántas memorias tiene cada uno. Uso "/list-context". Trigger cuando el usuario quiere ver qué proyectos existen en la memoria, listar proyectos, "qué proyectos tengo", "muéstrame la memoria", "en qué proyectos estoy trabajando", o no recuerda el slug exacto de un proyecto. Agnóstico: cualquier proyecto.
---

# list-context

Da una vista de pájaro del store de `mnemo`: qué proyectos hay y su tamaño.

## Store

`$MEM = $MNEMO_DIR` o `~/.local/share/mnemo`. Si no existe, avisa y detente.

## Pasos

1. **Sincroniza** si hay remoto (`git -C $MEM remote`): `git -C $MEM pull --rebase --autostash`.
   Si falla o el rebase conflictúa, no lo resuelvas aquí (esto es solo lectura): menciónalo en el
   resumen y sigue con lo local.

2. **Reúne los proyectos** de `$MEM/projects/*/INDEX.md`. Para cada uno lee del frontmatter:
   `slug`, `name`, `status`, `services`, `updated`.

3. **Cuenta memorias por proyecto**: para cada slug, cuántas memorias de `$MEM/memories/` lo
   tienen en su `projects` (usa `grep -rl` como filtro y afina leyendo el frontmatter). Marca
   cuántas de esas son **compartidas** (tagueadas con más de un proyecto).

4. **Presenta una tabla** ordenada por `status` (active primero) y luego por `updated` desc:

   | proyecto (slug) | estado | memorias | servicios | actualizado |
   |---|---|---|---|---|

   Si una memoria es compartida, refléjalo (p. ej. "8 (3 compartidas)").

5. **Resumen final** en una línea: total de proyectos, total de memorias, cuántas viven en
   `shared/`. Si el store está vacío (sin proyectos), dilo y recuerda que se crea el primero con
   `/save-context <slug>`.

## Notas

- Solo lectura. No escribe ni commitea.
- No cargues el contenido de ningún proyecto; esto es solo el índice. Para entrar a uno es
  `/load-context <slug>`.
