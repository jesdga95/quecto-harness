import assert from "node:assert/strict";
import test from "node:test";
import { createServer } from "vite";

test("An MCP probe distinguishes a missing tool list from an empty one", async (t) => {
  const vite = await createServer({
    configFile: false,
    optimizeDeps: { noDiscovery: true },
    server: { middlewareMode: true, ws: false, watch: null },
  });
  t.after(() => vite.close());
  const { httpApi } = await vite.ssrLoadModule("/src/lib/api/http.ts");
  const reply = t.mock.method(globalThis, "fetch");
  for (const tools of [undefined, null, {}, [], [{ name: "echo", description: "" }]]) {
    reply.mock.mockImplementation(() => Response.json({ ok: true, tools }));
    const result = await httpApi.probeMcp({ name: "local" });
    assert.deepEqual(result.tools, Array.isArray(tools) ? tools : undefined);
  }
});
