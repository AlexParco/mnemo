// The store's SCHEMA.md is written from the repo's templates/, so the packaged
// server and the plugin cannot drift apart. `assets/` is a build artifact here
// and a shipped file in the published package.
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const src = path.resolve(here, "..", "..", "templates", "SCHEMA.md");
const dest = path.resolve(here, "..", "assets", "SCHEMA.md");

fs.mkdirSync(path.dirname(dest), { recursive: true });
fs.copyFileSync(src, dest);
process.stderr.write(`assets: ${path.relative(process.cwd(), dest)}\n`);
