import assert from "node:assert/strict";
import test from "node:test";
import { createServer } from "vite";

async function loadApp(t) {
  const vite = await createServer({
    server: { middlewareMode: true, hmr: false, watch: null },
    appType: "custom",
    define: { "navigator.platform": JSON.stringify("Linux"), "import.meta.env.VITE_MOCK": JSON.stringify("0") },
    logLevel: "error",
  });
  t.after(() => vite.close());
  const { removeProject, app } = await vite.ssrLoadModule("/src/lib/state/app.svelte.ts");
  const { toasts } = await vite.ssrLoadModule("/src/lib/state/toast.svelte.ts");
  return { removeProject, app, toasts };
}

const retained = { path: "/scratch/work", reason: "its volume has no trash this account can use" };

test("A removed project shows each folder its deletes kept", async (t) => {
  const { removeProject, app, toasts } = await loadApp(t);
  t.mock.method(globalThis, "fetch", async (url, init) => {
    if (init.method === "DELETE") return Response.json({ projects: [], sessions_deleted: 1, retained_folders: [retained] });
    return Response.json({ sessions: [] });
  });
  await removeProject("/project");
  assert.deepEqual(app.projects, []);
  assert.equal(toasts.items.length, 1);
  assert.equal(toasts.items[0].path, retained.path);
  assert.match(toasts.items[0].text, /The folder stayed in place \(its volume has no trash/);
});

test("A project removal that stops partway reports what it deleted", async (t) => {
  const { removeProject, app, toasts } = await loadApp(t);
  const requests = [];
  t.mock.method(globalThis, "fetch", async (url, init) => {
    requests.push([init.method, url]);
    if (init.method === "DELETE") {
      return Response.json(
        { error: "Could not save the configuration", sessions_deleted: 2, retained_folders: [retained] },
        { status: 400 },
      );
    }
    return Response.json({ sessions: [] });
  });
  await removeProject("/project");
  assert.equal(app.error, "Could not save the configuration");
  const texts = toasts.items.map((item) => item.text);
  assert.ok(texts.includes("Could not save the configuration. 2 sessions were deleted before it stopped."), texts.join("\n"));
  assert.ok(toasts.items.some((item) => item.path === retained.path));
  assert.ok(requests.some(([method]) => method === "GET"), "the session list was not read again");
});

test("An unverified move shows the trash path after deleting a project", async (t) => {
  const { removeProject, toasts } = await loadApp(t);
  const failure = { ...retained, waiting: true, trash: "/trash/work", reason: "could not verify the entry" };
  t.mock.method(globalThis, "fetch", async (url, init) =>
    Response.json(init.method === "DELETE" ? { projects: [], retained_folders: [failure] } : { sessions: [] }),
  );
  await removeProject("/project");
  assert.equal(toasts.items[0].path, failure.path);
  assert.match(toasts.items[0].text, /Move awaiting verification.*Trash path to check: \/trash\/work/);
});
