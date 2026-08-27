/** P1 acceptance gate: the TypeScript card must reproduce `card.py` byte for
 * byte. Every fixture project, both languages, both sides of the machine flag.
 * If this fails, the port is not done — it is not a formatting preference. */

import assert from "node:assert/strict";
import test, { describe } from "node:test";
import { renderCard, ProjectNotFoundError } from "../src/card/render.js";
import { FIXTURE_PROJECTS, FIXTURE_STORE, hasPython, runCardPy } from "./helpers.js";

const LANGS = ["en", "es"] as const;
// One machine matches the fixture's `[@...]` stamps, the other does not.
const MACHINES = ["fixture-box", "other-box"] as const;

describe("card parity with card.py", { skip: hasPython() ? false : "python3 not available" }, () => {
  for (const slug of FIXTURE_PROJECTS) {
    for (const lang of LANGS) {
      for (const machine of MACHINES) {
        test(`${slug} · ${lang} · @${machine}`, () => {
          const py = runCardPy(FIXTURE_STORE, slug, lang, machine);
          assert.equal(py.status, 0, `card.py failed: ${py.stderr}`);
          const ts = renderCard(FIXTURE_STORE, slug, { lang, machine }) + "\n";
          assert.equal(ts, py.stdout);
        });
      }
    }
  }

  test("missing project: same message, same exit code", () => {
    const py = runCardPy(FIXTURE_STORE, "no-such-project", "en", "fixture-box");
    assert.equal(py.status, 1);
    assert.throws(
      () => renderCard(FIXTURE_STORE, "no-such-project", { lang: "en", machine: "fixture-box" }),
      (err: unknown) => {
        assert.ok(err instanceof ProjectNotFoundError);
        assert.equal(err.message + "\n", py.stderr);
        return true;
      },
    );
  });
});
