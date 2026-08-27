/** Shared shape for tool responses. */

export type ToolResult = {
  content: Array<{ type: "text"; text: string }>;
  isError?: boolean;
};

export const ok = (...blocks: string[]): ToolResult => ({
  content: blocks.map((text) => ({ type: "text", text })),
});

export const fail = (text: string): ToolResult => ({
  content: [{ type: "text", text }],
  isError: true,
});

export const json = (value: unknown): string => JSON.stringify(value, null, 2);

export function isToolResult(v: unknown): v is ToolResult {
  return typeof v === "object" && v !== null && "content" in v;
}

/** Turn a thrown error into a refusal the agent can act on. Errors from the
 * store and from git already carry actionable messages; anything else is a bug
 * and says so rather than pretending the write succeeded. */
export async function guarded(fn: () => Promise<ToolResult>): Promise<ToolResult> {
  try {
    return await fn();
  } catch (err) {
    if (err instanceof Error && (err.name === "StoreError" || err.name === "GitError" || err.name === "LockTimeoutError")) {
      return fail(err.message);
    }
    return fail(`mnemo failed unexpectedly: ${err instanceof Error ? err.message : String(err)}`);
  }
}
