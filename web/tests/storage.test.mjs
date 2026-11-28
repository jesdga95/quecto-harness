import assert from "node:assert/strict";
import test from "node:test";
import { createServer } from "vite";

async function load(t) {
  const vite = await createServer({
    server: { middlewareMode: true, hmr: false, watch: null },
    appType: "custom",
    define: { "navigator.platform": JSON.stringify("Linux"), "import.meta.env.VITE_MOCK": JSON.stringify("0") },
    logLevel: "error",
  });
  t.after(() => vite.close());
  const { api } = await vite.ssrLoadModule("/src/lib/api/index.ts");
  const { notice, heading } = await vite.ssrLoadModule("/src/lib/storage.ts");
  return { api, notice, heading };
}

test("Storage keeps the kind, record and trash path of each notice", async (t) => {
  const { api } = await load(t);
  const wire = [
    { path: "/scratch/a", reason: "it changed after its conversation was deleted", kept: true },
    { path: "/scratch/b", reason: "permission denied, and its record could not be read", record: "/state/b.owner.json" },
    { path: "/scratch/c", reason: "it could not be checked", waiting: true, trash: "/home/.Trash/c" },
    { path: "/scratch/d", reason: "another entry took its place", foreign: true, trash: "/home/.Trash/d", kept: "yes" },
    { path: "/project/note", reason: "its entry in the trash has not been read back yet", waiting: true, file: true, trash: "/t" },
    { reason: "no path" },
  ];
  t.mock.method(globalThis, "fetch", async () => Response.json({ trash_failures: wire }));
  assert.deepEqual(await api.trashFailures(), [
    { path: "/scratch/a", reason: "it changed after its conversation was deleted", kept: true },
    { path: "/scratch/b", reason: "permission denied, and its record could not be read", record: "/state/b.owner.json" },
    { path: "/scratch/c", reason: "it could not be checked", waiting: true, trash: "/home/.Trash/c" },
    { path: "/scratch/d", reason: "another entry took its place", foreign: true, trash: "/home/.Trash/d" },
    { path: "/project/note", reason: "its entry in the trash has not been read back yet", waiting: true, file: true, trash: "/t" },
  ]);
});

test("Storage says what each kind of notice means", async (t) => {
  const { notice } = await load(t);
  const kept = notice({ path: "/a", reason: "it changed after its conversation was deleted", kept: true });
  assert.equal(kept.status, "Kept: it changed after its conversation was deleted.");
  assert.equal(kept.advice, "Delete it yourself when you no longer need it.");
  assert.equal(kept.copy, "Copy folder path");
  assert.equal(kept.record, undefined);

  const damaged = notice({ path: "/b", reason: "permission denied, and its record could not be read", record: "/r" });
  assert.equal(damaged.status, "Move failed: permission denied, and its record could not be read.");
  assert.match(damaged.record, /inspect or repair/);
  assert.equal(damaged.advice, undefined);
  const unreadable = notice({ path: "/b", reason: "its notice could not be read", waiting: true, record: "/r" });
  assert.match(unreadable.status, /^Not checked yet: /);
  const notice_ = notice({ path: "/s/x.foreign.json", reason: "this notice could not be read", waiting: true, record: "/s/x.foreign.json" });
  assert.equal(notice_.copy, "Copy file path");
  assert.equal(notice_.record, undefined);

  const held = notice({ path: "/c", reason: "it changed", kept: true, record: "/order" });
  assert.equal(held.advice, undefined);
  assert.ok(held.record);

  assert.match(notice({ path: "/d", reason: "x", waiting: true, trash: "/t" }).status, /^Move awaiting verification/);
  assert.equal(notice({ path: "/d", reason: "x", waiting: true, trash: "/t" }).trash, "Trash path to check:");
  const foreign = notice({ path: "/e", reason: "x", foreign: true, trash: "/t" });
  assert.match(foreign.status, /^Another entry/);
  assert.equal(foreign.trash, "That entry is now in the trash at:");
});

test("Deleting a session preserves an unverified trash move", async (t) => {
  const { api } = await load(t);
  const failure = { path: "/scratch/work", trash: "/trash/work", waiting: true, reason: "could not verify the entry" };
  t.mock.method(globalThis, "fetch", () => Response.json(failure));
  assert.deepEqual(await api.deleteSession("session"), failure);
});

test("Storage names a file notice as a file", async (t) => {
  const { notice, heading } = await load(t);
  const file = { path: "/project/note", reason: "x", waiting: true, file: true };
  assert.equal(notice(file).copy, "Copy file path");
  assert.equal(heading([file]), "Folders and files to check");
  assert.equal(heading([{ path: "/s/notice.json", record: "/s/notice.json", reason: "damaged" }]), "Folders and files to check");
  assert.equal(heading([{ path: "/a", reason: "x", kept: true }]), "Folders to check");
});
