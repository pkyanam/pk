// M1 demo plugin for the v2 extension layer (docs/v2-architecture.md).
//
// Contract: when run directly (`bun run plugin.ts`), this script prints a
// single JSON line — {"tools":[...]} — describing the tools it registers.
// The Go side (internal/v2/ext) shells out to Bun, parses that line, and
// hot-reloads whenever this file is created, edited, or deleted: the M1
// demo is "write a tool → invoke it the same turn".
interface Tool {
  name: string;
  description: string;
  parameters: Record<string, unknown>;
}

const tools: Tool[] = [
  {
    name: "pk2_echo",
    description: "Echoes back the message provided as an argument.",
    parameters: {
      type: "object",
      properties: {
        message: { type: "string", description: "Text to echo back." },
      },
      required: ["message"],
    },
  },
];

if (import.meta.main) {
  console.log(JSON.stringify({ tools }));
}

export { tools };