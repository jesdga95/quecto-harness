import assert from "node:assert/strict";
import { setImmediate } from "node:timers/promises";
import test from "node:test";
import { createServer } from "vite";

async function loadDeletionDialog(t) {
  const vite = await createServer({
    server: { middlewareMode: true, hmr: false, watch: null },
    appType: "custom",
    define: { "navigator.platform": JSON.stringify("Linux"), "import.meta.env.VITE_MOCK": JSON.stringify("0") },
    logLevel: "error",
  });
  t.after(() => vite.close());
  const { deleteSessionAsked, app } = await vite.ssrLoadModule("/src/lib/state/app.svelte.ts");
  const { dialog, answer } = await vite.ssrLoadModule("/src/lib/state/confirm.svelte.ts");
  const { toasts, dismissConnectionWarnings } = await vite.ssrLoadModule("/src/lib/state/toast.svelte.ts");
  return { deleteSessionAsked, dialog, answer, toasts, connected: dismissConnectionWarnings, app };
}

test("A changed deletion needs a second confirmation before reporting recovery", async (t) => {
  const { deleteSessionAsked, dialog, answer, toasts } = await loadDeletionDialog(t);
  const requests = [];
  const initial = {
    kept: "/scratch",
    deferred: true,
    carry_token: "first",
    carry: { destination: "/project", count: 0, record: "/state/copy", unreadable: true, damaged_line: 3 },
  };
  const updated = {
    kept: "/scratch",
    carry_token: "second",
    carry: { destination: "/project", count: 1, names: ["note"], recovery: "/state/recovery" },
  };
  const deletes = [];
  t.mock.method(globalThis, "fetch", async (url, init) => {
    requests.push([init.method, url]);
    if (url.endsWith("/kept")) return Response.json(deletes.length ? updated : initial);
    if (init.method === "DELETE") {
      deletes.push(JSON.parse(init.body));
      return deletes.length === 1
        ? Response.json({ error: "Files changed", code: "kept_changed" }, { status: 409 })
        : new Response(null, { status: 204 });
    }
    return Response.json({ sessions: [] });
  });

  const deletion = deleteSessionAsked("example", true);
  await setImmediate();
  assert.match(dialog.current.body, /no session uses it/);
  assert.match(dialog.current.body, /is damaged at line 3/);
  assert.equal(deletes.length, 0);
  answer(true);
  await setImmediate();
  assert.deepEqual(requests, [
    ["GET", "/api/sessions/example/kept"],
    ["DELETE", "/api/sessions/example"],
    ["GET", "/api/sessions/example/kept"],
  ]);
  assert.match(dialog.current.body, /Files changed/);
  assert.match(dialog.current.body, /will be saved in \/state\/recovery/);
  assert.deepEqual(dialog.current.files, ["note"]);
  assert.deepEqual(toasts.items, []);
  answer(true);
  await deletion;
  assert.deepEqual(deletes, [
    { kept: "/scratch", deferred: true, carry_token: "first" },
    { kept: "/scratch", deferred: false, carry_token: "second" },
  ]);
  assert.equal(toasts.items[0].text, "The recovery list is saved in /state/recovery.");
});

test("A refused folder move shows the recovery path before retrying", async (t) => {
  const { deleteSessionAsked, dialog, answer } = await loadDeletionDialog(t);
  const requests = [];
  const message = "Move /scratch/.removal-0123456789ab to an unused location, then retry.";
  t.mock.method(globalThis, "fetch", async (url, init) => {
    requests.push(init.method);
    if (init.method === "DELETE") return Response.json({ error: message, code: "aside_recovery" }, { status: 409 });
    return Response.json({ kept: "/scratch/work" });
  });
  const deletion = deleteSessionAsked("example", true);
  await setImmediate();
  answer(true);
  await setImmediate();
  assert.equal(dialog.current?.body, message);
  answer(true);
  await setImmediate();
  assert.deepEqual(requests, ["GET", "DELETE", "GET"]);
  assert.equal(dialog.current.title, "Delete this session?");
  answer(false);
  await deletion;
});

test("A completed deletion keeps a copyable trash warning after reconnecting", async (t) => {
  const { deleteSessionAsked, dialog, answer, toasts, connected, app } = await loadDeletionDialog(t);
  const path = "/scratch/kept";
  const reason = "Permission denied";
  app.sessions = { ":no-folder": [{ id: "example", scratch: true }] };
  t.mock.method(globalThis, "fetch", async (url, init) => {
    if (url.endsWith("/kept")) return Response.json({ kept: "" });
    if (init.method === "DELETE") return Response.json({ path, reason });
    return Response.json({ sessions: [] });
  });
  const deletion = deleteSessionAsked("example", true);
  await setImmediate();
  assert.equal(dialog.current.title, "Delete this session?");
  answer(true);
  await deletion;
  assert.deepEqual(app.sessions[":no-folder"], []);
  connected();
  assert.equal(toasts.items.length, 1);
  assert.equal(toasts.items[0].path, path);
  assert.equal(toasts.items[0].sticky, true);
  assert.match(toasts.items[0].text, /Permission denied/);
});
