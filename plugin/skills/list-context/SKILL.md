---
name: list-context
description: Show the overview of the memory store — every project with its status, services and how many memories each one has. Usage/Uso: "/mnemo:list-context". Triggers: user wants to see which projects exist in memory, list projects, "what projects do I have", "show me the memory", "which projects am I working on", or doesn't remember a project's exact slug, plus "qué proyectos existen en la memoria", "listar proyectos", "qué proyectos tengo", "muéstrame la memoria", "en qué proyectos estoy trabajando". Works for any project / sirve para cualquier proyecto.
---

# list-context

A bird's-eye view of the store: which projects exist and how big each one is.

If the `mnemo_*` tools are not available, the MCP server is not running — say so rather than
reaching for the filesystem.

**Output language:** write all user-facing output in the language the user is writing in (Spanish
or English).

## Steps

1. Call `mnemo_sync` if you want the counts to include the other machines' latest. It is optional
   here: if it reports a problem, mention it in one line and carry on with the local copy, because
   this flow is read-only and does not resolve conflicts.

2. Call `mnemo_list_projects` and show its table and summary line.

## Notes

- **Compact output:** the table and the summary line, nothing else. No surrounding prose, no
  describing each project — for the detail of one, that is `/mnemo:load-context <slug>`.
- Read-only. Never writes, never commits.
- If the store is empty, say so and mention that the first project is created by a save.
