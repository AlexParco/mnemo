/** `pending.md` is free-form by contract: the card renders whatever sections
 * exist. The parser must therefore preserve, not normalise. */

import assert from "node:assert/strict";
import test, { describe } from "node:test";
import { machineFlag, openItems, parsePending, IN_PROGRESS, NEXT } from "../src/store/pending.js";

describe("parsePending", () => {
  test("keeps unknown sections, in file order", () => {
    const sections = parsePending("## Next\n- [ ] a\n\n## Wild Section\n- [x] b\n\n## Blocked\n- [ ] c\n");
    assert.deepEqual(sections.map((s) => s.label), ["Next", "Wild Section", "Blocked"]);
  });

  test("only checkbox items count, and done state is read", () => {
    const [section] = parsePending("## Next\n- [ ] open\n- [x] closed\n- [X] also closed\n- plain bullet\n- [-] not a checkbox\n");
    assert.deepEqual(section!.items, [
      { text: "open", done: false },
      { text: "closed", done: true },
      { text: "also closed", done: true },
    ]);
  });

  test("items before any heading are dropped", () => {
    assert.deepEqual(parsePending("- [ ] orphan\n## Next\n- [ ] kept\n").map((s) => s.items.length), [1]);
  });

  test("a repeated heading accumulates into one section", () => {
    const sections = parsePending("## Next\n- [ ] a\n\n## Other\n\n## next\n- [ ] b\n");
    assert.equal(sections.length, 2);
    assert.deepEqual(sections[0]!.items.map((i) => i.text), ["a", "b"]);
  });
});

describe("core section order", () => {
  // card.py holds these in Python sets, so with both variants present its order
  // is unspecified across processes. The port fixes it.
  const mixed = "## En curso\n- [ ] spanish\n\n## In progress\n- [ ] english\n";

  test("is deterministic when both language variants appear", () => {
    for (let i = 0; i < 20; i++) {
      assert.deepEqual(openItems(parsePending(mixed), IN_PROGRESS), ["english", "spanish"]);
    }
  });

  test("english is the canonical first variant", () => {
    assert.deepEqual(IN_PROGRESS, ["in progress", "en curso"]);
    assert.deepEqual(NEXT, ["next", "siguiente"]);
  });

  test("done items never reach the resume list", () => {
    assert.deepEqual(openItems(parsePending("## Next\n- [x] done\n- [ ] open\n"), NEXT), ["open"]);
  });
});

describe("machineFlag", () => {
  test("flags only other machines, ignoring padding", () => {
    assert.equal(machineFlag("push the repo [@laptop]", "laptop"), "");
    assert.equal(machineFlag("push the repo [@ laptop ]", "laptop"), "");
    assert.equal(machineFlag("push the repo [@desktop]", "laptop"), "⚠ ");
    assert.equal(machineFlag("no stamp at all", "laptop"), "");
  });
});
