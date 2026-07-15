---
name: mem
description: Guarda una nota suelta en la memoria persistente a mitad de sesión, tagueada por proyecto, sin cerrar ni sincronizar todo. Uso "/mnemo:mem <proyecto>[,proyecto2] <la nota>". Trigger cuando el usuario quiere apuntar rápido una decisión, gotcha o dato en la memoria de uno o varios proyectos, "apunta que...", "recuerda para X que...", sin hacer un save-context completo. Agnóstico: cualquier proyecto.
---

# mem

Atajo para capturar una sola memoria atómica sin el flujo completo de `/mnemo:save-context`.

## Store

`$MEM = $MNEMO_DIR` o `~/.local/share/mnemo`. Si aún no existe (o el proyecto no existe todavía),
`/mnemo:mem` no bootstrapea: dile al usuario que arranque con `/mnemo:save-context <slug>` y detente.

## Pasos

0. **Sincroniza** si hay remoto: `git -C $MEM pull --rebase --autostash`. Si conflictúa, fusiona
   conservando ambos lados (o para y pregunta si el choque es semántico) antes de escribir, y
   continúa con `GIT_EDITOR=true git -C $MEM rebase --continue` — sin `GIT_EDITOR` la shell se
   cuelga en el editor.

1. **Parsea el argumento.** Primer token = proyecto(s), separados por coma para overlap
   (`rappi-f3,inventory-hotfix`). El resto = contenido de la nota. Si no hay proyecto claro,
   pregúntalo (ofrece los proyectos existentes de `$MEM/projects`).

2. **Verifica que los proyectos existan** en `$MEM/projects/`. Si alguno no existe, avísalo y
   pregunta si crearlo o corregir el slug (no lo crees en silencio).

3. **Escribe una memoria** en `$MEM/memories/<id>.md` según `shared/SCHEMA.md`:
   - `id` kebab-case derivado del contenido, único (revisa que no exista; si el tema ya existe,
     actualiza esa memoria).
   - `projects: [...]` con todos los slugs dados (overlap).
   - Infiere `type` y `services` del contenido; `updated` (hoy) y `author`
     (`git -C $MEM config user.name`).

4. **Commit local** `git -C $MEM add -A && git -C $MEM commit -m "mem(<slugs>): <resumen>"`.
   No hagas push aquí — el push se hace en `/mnemo:save-context` (o si el usuario lo pide explícito).
   Sin `Co-Authored-By`.

5. Confirma en una línea qué se guardó y dónde.
