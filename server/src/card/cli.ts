#!/usr/bin/env node
/** Drop-in replacement for `card.py`: same argv, same stdout, same exit codes.
 * Lets the Claude Code plugin stop depending on python3 once parity holds. */

import { renderCard, resolveLang, ProjectNotFoundError } from "./render.js";
import { storeDir } from "../store/paths.js";

const slug = process.argv[2];
if (!slug) {
  process.stderr.write("usage: card <slug> [lang]\n");
  process.exit(2);
}

const store = storeDir();
try {
  process.stdout.write(renderCard(store, slug, { lang: resolveLang(process.argv[3]) }) + "\n");
} catch (err) {
  if (err instanceof ProjectNotFoundError) {
    process.stderr.write(err.message + "\n");
    process.exit(1);
  }
  throw err;
}
