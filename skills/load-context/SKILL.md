---
name: load-context
description: Carga la memoria persistente de un proyecto/iniciativa al inicio de una sesión de Claude — decisiones, restricciones, gotchas y tareas pendientes — para retomar el trabajo donde se quedó. Uso "/load-context <proyecto>". Trigger cuando el usuario pide cargar el contexto de un proyecto, retomar un proyecto, "carga el contexto de X", "seguir con X", "en qué quedamos en X", o abre una sesión nueva para seguir una iniciativa previa. Agnóstico: sirve para cualquier proyecto.
---

# load-context

Recupera la memoria de un proyecto desde el store git de `mnemo` y deja al asistente
listo para continuar el trabajo.

## Resolución del store

Directorio del store = `$MNEMO_DIR` si está definido, si no `~/.local/share/mnemo`.
Llámalo `$MEM` de aquí en adelante. Si no existe, dile al usuario que clone/instale el repo
(ver su README) y detente.

## Pasos

0. **El slug es obligatorio.** Si el usuario no pasó proyecto, **no cargues nada**: ejecuta el
   comportamiento de `/list-context` (muestra el panorama de proyectos) y pídele que elija uno.
   No adivines ni cargues "el último".

1. **Sincroniza.** Si el store tiene remoto (`git -C $MEM remote`), corre
   `git -C $MEM pull --rebase --autostash` para traer lo que guardaste desde otras máquinas.
   - Si no hay remoto, o el pull falla por red/acceso, avisa en una línea y sigue con lo local.
   - Si el rebase se detiene por conflicto, **para y avísale al usuario**: este skill es de solo
     lectura y no resuelve conflictos. Que corra `/save-context` (que sí los fusiona) o los
     resuelva a mano. Deja el rebase como está, no lo abortes en silencio.

2. **Resuelve el proyecto.** El argumento es un slug. Verifica `$MEM/projects/<slug>/`.
   - Si no existe, lista los proyectos disponibles (`ls $MEM/projects`) con su `status` y
     pregunta a cuál se refería. No inventes un proyecto.

3. **Reúne el contexto** (leer, no escribir):
   - `$MEM/projects/<slug>/INDEX.md` — qué es, alcance, estado, servicios.
   - `$MEM/projects/<slug>/pending.md` — tareas pendientes.
   - **Todas** las memorias de `$MEM/memories/` cuyo frontmatter `projects` **incluya el slug**.
     Búscalas con: `grep -rl "projects:.*<slug>" $MEM/memories/` y afina leyendo el frontmatter
     (el slug debe estar en la lista `projects`, no ser substring de otro). Aquí es donde el
     overlap importa: una memoria tagueada con varios proyectos entra si el slug está presente.
   - `$MEM/shared/` — convenciones globales que siempre aplican.

4. **Resume para el usuario**, conciso y accionable:
   - 1-2 líneas: qué es el proyecto y su estado.
   - Servicios/áreas que toca.
   - Decisiones y restricciones clave (bullet corto por memoria relevante; agrupa por `type`).
   - **Pendientes**: lo que estaba en curso y lo siguiente.
   - Cierra preguntando en qué tarea quiere continuar.

5. **No cargues otros proyectos.** Solo el pedido. Si una memoria pertenece también a otro
   proyecto, úsala igual (overlap) pero no arrastres el resto de ese otro proyecto.

## Notas

- Solo lectura. Este skill nunca escribe ni commitea. Para guardar, es `/save-context`.
- Sin slug → cae en modo lista (paso 0), nunca carga por defecto.
