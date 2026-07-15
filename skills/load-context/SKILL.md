---
name: load-context
description: Carga la memoria persistente de un proyecto/iniciativa al inicio de una sesión de Claude — decisiones, restricciones, gotchas y tareas pendientes — para retomar el trabajo donde se quedó. Uso "/mnemo:load-context <proyecto>". Trigger cuando el usuario pide cargar el contexto de un proyecto, retomar un proyecto, "carga el contexto de X", "seguir con X", "en qué quedamos en X", o abre una sesión nueva para seguir una iniciativa previa. Agnóstico: sirve para cualquier proyecto.
---

# load-context

Recupera la memoria de un proyecto desde el store git de `mnemo` y deja al asistente
listo para continuar el trabajo.

## Resolución del store

Directorio del store = `$MNEMO_DIR` si está definido, si no `~/.local/share/mnemo`.
Llámalo `$MEM` de aquí en adelante.

**Si `$MEM` no existe o no es repo git, adopta primero el store compartido:**
- `MNEMO_REMOTE` seteado → clónalo del hub: `git clone "$MNEMO_REMOTE" "$MEM"`. Trae toda la
  memoria de tus otras máquinas (sirve para remoto SSH o ruta local). Luego seguí normal.
- Sin `MNEMO_REMOTE` → no hay memoria que cargar: sugiere `/mnemo:save-context <slug>` y detente.

## Pasos

0. **El slug es obligatorio.** Si el usuario no pasó proyecto, **no cargues nada**: ejecuta el
   comportamiento de `/mnemo:list-context` (muestra el panorama de proyectos) y pídele que elija uno.
   No adivines ni cargues "el último".

1. **Sincroniza.** Si el store tiene remoto (`git -C $MEM remote`), corre
   `git -C $MEM pull --rebase --autostash` para traer lo que guardaste desde otras máquinas.
   - Si no hay remoto, o el pull falla por red/acceso, avisa en una línea y sigue con lo local.
   - Si el rebase se detiene por conflicto, **para y avísale al usuario**: este skill es de solo
     lectura y no resuelve conflictos. Que corra `/mnemo:save-context` (que sí los fusiona) o los
     resuelva a mano. Deja el rebase como está, no lo abortes en silencio.

2. **Resuelve el proyecto.** El argumento es un slug. Verifica `$MEM/projects/<slug>/`.
   - Si no existe, lista los proyectos disponibles (`ls $MEM/projects`) con su `status` y
     pregunta a cuál se refería. No inventes un proyecto.

3. **Reúne el contexto** (leer, no escribir). Leelo **todo** para tenerlo en la sesión —
   pero NO lo imprimas entero; el paso 4 define qué se muestra.
   - `$MEM/projects/<slug>/INDEX.md` — qué es, alcance, estado, servicios.
   - `$MEM/projects/<slug>/pending.md` — tareas pendientes.
   - **Todas** las memorias de `$MEM/memories/` cuyo frontmatter `projects` **incluya el slug**.
     Búscalas con: `grep -rl "projects:.*<slug>" $MEM/memories/` y afina leyendo el frontmatter
     (el slug debe estar en la lista `projects`, no ser substring de otro). Aquí es donde el
     overlap importa: una memoria tagueada con varios proyectos entra si el slug está presente.
   - `$MEM/shared/` — convenciones globales que siempre aplican.

4. **Mostrá la tarjeta con el script (formato estricto).** El formato NO lo redactás vos: lo
   genera un script determinista. Corré:

   ```bash
   MNEMO_DIR="$MEM" python3 "${CLAUDE_SKILL_DIR}/card.py" <slug>
   ```

   y mostrá su **salida tal cual, como la respuesta completa** — sin agregar prosa antes ni
   después. Ya leíste todo en el paso 3 (tenés el detalle en contexto para trabajar), pero acá
   **solo se muestra la tarjeta**.
   - Si el usuario pide "el detalle" / "las decisiones" / "qué había de X", **ahí sí** desplegás lo
     relevante de lo que leíste en el paso 3. Por default, la tarjeta y nada más.
   - **Fallback:** si el script sale con error (≠0) o no hay `python3`, mostrá a mano un resumen
     mínimo equivalente: `📁 <slug> · <status>`, `▶ Retomar por: …`, los pendientes de `pending.md`
     y `🗄 <N> memorias`. Nada de volcar decisiones/deuda.

5. **No cargues otros proyectos.** Solo el pedido. Si una memoria pertenece también a otro
   proyecto, úsala igual (overlap) pero no arrastres el resto de ese otro proyecto.

## Conciencia de máquina

La memoria es compartida entre máquinas, pero **algunos ítems son de UNA máquina** (código local,
"commitear/pushear el repo X"). Van estampados `[@<máquina>]` y el render los marca con **⚠** cuando
no son de esta máquina (actual = `${MNEMO_MACHINE:-$(hostname -s)}`).

**Regla: no actúes sobre trabajo que no es de esta máquina.** Si el usuario pide commitear/pushear/
correr algo que corresponde a un ítem `⚠ [@otra]` —o cuyo repo de código **no existe en esta
máquina**— **no busques el repo ni intentes la acción**: decile claro *"esto es de `<máquina>`; el
código no está en esta (`<actual>`)"*. Antes de cualquier acción de git/build sobre un repo de
código, verificá que exista localmente. (Esto es aparte del store de mnemo, que sí está en todas.)

## Notas

- Solo lectura. Este skill nunca escribe ni commitea. Para guardar, es `/mnemo:save-context`.
- Sin slug → cae en modo lista (paso 0), nunca carga por defecto.
