/** Repeated write+commit cycles against a store, as its own process.
 *
 * The loop is the point. One write per process staggers itself on node's startup
 * cost and proves nothing; several cycles per process put every writer on the
 * store at the same time, which is when a missing lock actually shows — git
 * refuses concurrent access to its own index, so an unguarded writer dies on
 * `index.lock` rather than quietly corrupting anything. */
import { commitStore, writeMemory } from "../../src/store/write.js";

const store = process.argv[2]!;
const prefix = process.argv[3]!;
const count = Number.parseInt(process.argv[4] ?? "1", 10);

for (let i = 0; i < count; i++) {
  const id = `${prefix}-${i}`;
  await writeMemory(store, { id, projects: ["orion-api"], type: "todo", body: `Written by ${id}.` });
  await commitStore(store, `save(orion-api): ${id}`);
}
