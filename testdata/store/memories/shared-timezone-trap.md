---
id: shared-timezone-trap
projects: [busy-api, paused-web]
services: [api, web]
type: gotcha
author: sam
updated: 2026-06-14
---
Both products store UTC but the admin UI renders in the browser locale.
