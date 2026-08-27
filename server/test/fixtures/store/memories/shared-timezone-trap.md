---
id: shared-timezone-trap
projects: [orion-api, atlas-web]
services: [api, web]
type: gotcha
author: Alexander Parco
updated: 2026-06-14
---
Both products store UTC but the admin UI renders in the browser locale.
