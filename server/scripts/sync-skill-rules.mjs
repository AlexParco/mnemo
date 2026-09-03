// Fill the <!-- mnemo:rule NAME --> regions in the plugin's SKILL.md files from
// src/prompts/criterion.ts, so the skills are a fourth delivery channel for the
// same rules rather than a fourth copy of them that quietly drifts.
//
//   node scripts/sync-skill-rules.mjs           rewrite the files
//   node scripts/sync-skill-rules.mjs --check   exit 1 if anything is stale
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const root = path.resolve(here, "..", "..");
const criterion = await import(path.join(here, "..", "dist", "src", "prompts", "criterion.js"));

const REGION = /(<!-- mnemo:rule ([A-Z_]+) -->\n)([\s\S]*?)(<!-- \/mnemo:rule -->)/g;
const check = process.argv.includes("--check");
let stale = 0;

for (const dir of fs.readdirSync(path.join(root, "skills"))) {
  const file = path.join(root, "skills", dir, "SKILL.md");
  if (!fs.existsSync(file)) continue;
  const before = fs.readFileSync(file, "utf8");
  const after = before.replace(REGION, (_m, open, name, _body, close) => {
    const rule = criterion[name];
    if (typeof rule !== "string") throw new Error(`${dir}: no rule named ${name} in criterion.ts`);
    return `${open}${rule}\n${close}`;
  });
  if (after === before) continue;
  stale++;
  if (check) process.stderr.write(`stale: skills/${dir}/SKILL.md\n`);
  else {
    fs.writeFileSync(file, after);
    process.stderr.write(`synced: skills/${dir}/SKILL.md\n`);
  }
}

if (check && stale > 0) {
  process.stderr.write("\nRun: node server/scripts/sync-skill-rules.mjs\n");
  process.exit(1);
}
process.stderr.write(check ? "skills are in sync\n" : `${stale} file(s) updated\n`);
