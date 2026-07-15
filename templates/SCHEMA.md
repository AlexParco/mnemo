# Contrato de datos

## Memoria atómica — `memories/<id>.md`

```markdown
---
id: <kebab-case-unico>          # = nombre del archivo sin .md
projects: [<slug>, <slug2>]     # 1+ proyectos. AQUÍ vive el overlap.
services: [<opcional>]          # áreas/servicios/módulos que toca (opcional)
tags: [<opcional>]              # etiquetas libres para búsqueda (opcional)
type: decision | constraint | gotcha | bug | reference | todo
author: <nombre>
updated: <YYYY-MM-DD>
---

Cuerpo en markdown. Un hecho por archivo, autoexplicativo y conciso.
Enlaza memorias relacionadas con [[otro-id]].
```

### Reglas

- **`id` == nombre del archivo.** `memories/checkout-customer-immutable.md` → `id: checkout-customer-immutable`.
- **`projects` es una lista, siempre.** Aunque sea un solo proyecto: `projects: [rappi-f3]`.
  Filtrar por proyecto = "¿el slug está en `projects`?". El overlap sale gratis.
- **Un hecho por archivo.** Si una nota mezcla dos temas, pártela. Facilita el re-tag y el diff.
- **Antes de crear, busca duplicado** por `id`/tema. Si existe, actualiza ese archivo y su `updated`.
- **`type`**: `decision` (se decidió X), `constraint` (invariante que no se debe romper),
  `gotcha` (trampa/no-obvio), `bug` (incidente/known issue), `reference` (puntero externo),
  `todo` (pendiente puntual; lo grande va en `pending.md`).

## Proyecto — `projects/<slug>/INDEX.md`

```markdown
---
slug: <kebab-case>
name: <nombre legible>
status: active | paused | done
services: [<áreas que toca>]
updated: <YYYY-MM-DD>
---

# <nombre>

Qué es el proyecto, objetivo, alcance. Contexto que no se deriva del código.
Servicios/áreas que toca y por qué. Decisiones macro con enlace a [[memoria-id]].
```

## Pendientes — `projects/<slug>/pending.md`

Lista viva del **estado del proyecto**. `/save-context` la actualiza; `/load-context` la lee para
retomar. Las secciones son **libres**: la tarjeta de `load-context` renderiza las que existan, así
que agregá solo las que apliquen al proyecto. `## En curso` y `## Siguiente` alimentan los
"Pendientes" numerados; el resto (`Bloqueado`, `Deuda`, `Desplegado`, …) se muestran como bloques.

```markdown
# Pendientes — <nombre>

## En curso
- [ ] <tarea> — <contexto/nota>

## Siguiente
- [ ] <tarea>

## Bloqueado                 <!-- opcional -->
- [ ] <tarea> — bloqueado por <razón>

## <sección propia del proyecto>   <!-- opcional, cuantas quieras -->
- [ ] <ítem>
```

Las secciones extra las elige cada proyecto según lo que ayude a retomar. Ejemplos según el tipo
de trabajo: `Deuda` (deuda técnica / known issues), `Ramas` (qué se pusheó a qué rama),
`Hecho`/`Desplegado` (lo ya cerrado, con items `- [x]`, para no repisar), `Riesgos`,
`Decisiones abiertas`… Ninguna es obligatoria; agregá solo las que apliquen.

**Ítems atados a una máquina.** La memoria se comparte entre máquinas, pero algunos ítems son de
UNA sola (código local sin commitear, "pushear el repo X", un servicio corriendo acá). Estampalos
con `[@<máquina>]` al final (`<máquina>` = `MNEMO_MACHINE` o el hostname). Vistos desde otra
máquina, `load-context` los marca con **⚠** y Claude no intenta actuar sobre ellos ahí. Las tareas
portables (implementar X, decisiones) no se estampan.
