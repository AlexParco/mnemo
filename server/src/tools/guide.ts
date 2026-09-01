/** `mnemo_guide`: the criterion as text to paste into a rules file.
 *
 * The channel of last resort, and the only one that reaches every host: a client
 * that implements neither prompts nor the `instructions` field still reads
 * AGENTS.md. Generated from the same constants as the prompts, so the copy the
 * user pastes cannot fall behind. */

import { z } from "zod";
import type { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import { guideText } from "../prompts/criterion.js";
import { ok } from "./result.js";

const TARGETS: Record<string, string> = {
  agents: "AGENTS.md",
  claude: "CLAUDE.md",
  cursor: ".cursor/rules/mnemo.md",
  generic: "your agent's rules file",
};

export function registerGuideTool(server: McpServer): void {
  server.registerTool(
    "mnemo_guide",
    {
      title: "mnemo: rules to paste into a project's agent instructions",
      description:
        "Return mnemo's usage criterion as markdown, for the user to paste into their agent's rules file (AGENTS.md, " +
        "CLAUDE.md, .cursor/rules). Use it when the user is setting mnemo up, or when they ask how their agent should " +
        "use it. Hosts that surface MCP prompts already get these rules that way; this is for the ones that do not.",
      inputSchema: {
        target: z
          .enum(["agents", "claude", "cursor", "generic"])
          .optional()
          .describe("Which rules file the snippet is destined for. Only changes the wording of the instruction, not the rules."),
      },
      annotations: { readOnlyHint: true },
    },
    async ({ target }) =>
      ok(
        `Paste the following into ${TARGETS[target ?? "generic"]}:`,
        guideText(),
      ),
  );
}
