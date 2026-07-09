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

Lista viva de lo que falta. `/save-context` la actualiza; `/load-context` la lee para
retomar. Formato libre pero recomendado:

```markdown
# Pendientes — <nombre>

## En curso
- [ ] <tarea> — <contexto/nota>

## Siguiente
- [ ] <tarea>

## Bloqueado
- [ ] <tarea> — bloqueado por <razón>
```
