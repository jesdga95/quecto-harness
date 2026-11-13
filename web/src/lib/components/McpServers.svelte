<script lang="ts">
  import Plus from "phosphor-svelte/lib/PlusIcon";
  import type { McpProbe, McpServer, McpServerRecord } from "../api";
  import { app, loadMcp, probeMcp, saveMcp } from "../state/app.svelte";
  import { confirm } from "../state/confirm.svelte";
  import OptionPicker from "./OptionPicker.svelte";
  import SplitDialog from "./SplitDialog.svelte";

  let { onclose, onback }: { onclose: () => void; onback?: () => void } = $props();

  const TRANSPORTS: { value: string; label: string; hint: string }[] = [
    { value: "stdio", label: "Stdio", hint: "The harness starts the command and talks to it over stdin and stdout." },
    { value: "http", label: "HTTP", hint: "The harness reaches a server that is already running at an address." },
  ];

  void loadMcp();

  // The saved server on screen, by name, or a new one that is not saved yet.
  // Edits land in the draft and reach mcp.json on Save.
  let chosen = $state("");
  let fresh = $state(false);
  let draft = $state<McpServerRecord | null>(null);
  let saving = $state(false);
  let testing = $state(false);
  let probe: McpProbe | null = $state(null);
  let body: HTMLElement | null = $state(null);

  const stored = $derived(fresh ? null : (app.mcp.find((s) => s.name === chosen) ?? app.mcp[0] ?? null));
  const baseline = $derived(fresh ? blank() : stored ? record(stored) : null);
  const dirty = $derived(draft !== null && JSON.stringify(draft) !== JSON.stringify(baseline));

  // The text fields hold what is typed and are filled one-way when another
  // server is shown, so a keystroke is never overwritten by its own commit.
  let command = $state("");
  let args = $state("");
  let cwd = $state("");
  let env = $state("");
  let url = $state("");
  let headers = $state("");
  let timeout = $state("");
  let paste = $state("");
  let pasteError = $state("");

  // The draft follows the list until a server is picked, made or pasted.
  $effect(() => {
    if (!fresh && stored && (draft === null || chosen === "")) {
      chosen = stored.name;
      show(record(stored));
    }
  });

  function show(d: McpServerRecord) {
    draft = d;
    probe = null;
    paste = "";
    pasteError = "";
    command = d.command;
    args = d.args.join("\n");
    cwd = d.cwd;
    env = pairs(d.env, "=");
    url = d.url;
    headers = pairs(d.headers, ": ");
    timeout = d.timeout_s ? String(d.timeout_s) : "";
  }

  // A snapshot, since a server read back from state is a proxy.
  function record(s: McpServer): McpServerRecord {
    const { problem: _p, state: _s, error: _e, tools: _t, running: _r, ...rest } = $state.snapshot(s);
    return rest;
  }

  function blank(): McpServerRecord {
    return {
      name: "",
      enabled: true,
      transport: "stdio",
      command: "",
      args: [],
      env: {},
      cwd: "",
      url: "",
      headers: {},
      timeout_s: 0,
      read_only: false,
      allow: [],
    };
  }

  function pairs(m: Record<string, string>, sep: string): string {
    return Object.entries(m)
      .map(([k, v]) => k + sep + v)
      .join("\n");
  }

  function parsePairs(text: string, sep: string): Record<string, string> {
    const out: Record<string, string> = {};
    for (const line of text.split("\n")) {
      const at = line.indexOf(sep);
      if (at <= 0) continue;
      const key = line.slice(0, at).trim();
      if (key) out[key] = line.slice(at + sep.length).trim();
    }
    return out;
  }

  function edit(patch: Partial<McpServerRecord>) {
    if (!draft) return;
    draft = { ...draft, ...patch };
    probe = null;
  }

  /** Answers whether it is alright to move off what is on screen. */
  async function letGo(): Promise<boolean> {
    if (!dirty) return true;
    return confirm({
      title: fresh ? "Discard the new server?" : `Discard changes to ${stored?.name}?`,
      body: "What was typed is not saved.",
      confirmLabel: "Discard",
    });
  }

  async function select(name: string) {
    if (!fresh && name === stored?.name) return;
    if (!(await letGo())) return;
    const s = app.mcp.find((x) => x.name === name);
    if (!s) return;
    fresh = false;
    chosen = name;
    show(record(s));
  }

  async function leave() {
    if (!(await letGo())) return;
    onclose();
  }

  async function back() {
    if (!(await letGo())) return;
    onback?.();
  }

  async function create() {
    if (fresh) return;
    if (!(await letGo())) return;
    fresh = true;
    show(blank());
  }

  async function save() {
    if (!draft || saving || !draft.name) return;
    saving = true;
    const mine = $state.snapshot(draft);
    const others = app.mcp.filter((s) => fresh || s.name !== stored?.name).map(record);
    const saved = await saveMcp([...others, mine]);
    saving = false;
    if (!saved) return;
    const back = saved.find((s) => s.name === mine.name);
    fresh = false;
    chosen = mine.name;
    if (back) show(record(back));
  }

  async function remove() {
    if (fresh) {
      if (!(await letGo())) return;
      fresh = false;
      chosen = "";
      draft = null;
      return;
    }
    if (!stored) return;
    const gone = stored.name;
    const ok = await confirm({
      title: `Delete ${gone}?`,
      body: "Its tools leave every turn, and a running instance stops.",
      confirmLabel: "Delete",
    });
    if (!ok) return;
    const saved = await saveMcp(app.mcp.filter((s) => s.name !== gone).map(record));
    if (!saved) return;
    chosen = "";
    draft = null;
  }

  async function test() {
    if (!draft || testing) return;
    testing = true;
    probe = null;
    body?.scrollTo({ top: 0, behavior: "smooth" });
    const result = await probeMcp($state.snapshot(draft));
    testing = false;
    probe = result ?? { ok: false, error: "The test did not run." };
  }

  // A snippet as servers publish it: an mcpServers object, or the map inside
  // one. One server fills the form; several are saved together.
  async function pasted(text: string) {
    pasteError = "";
    if (text.trim() === "") return;
    let raw: unknown;
    try {
      raw = JSON.parse(text);
    } catch {
      pasteError = "That is not valid JSON.";
      return;
    }
    const root = (raw as { mcpServers?: unknown })?.mcpServers ?? raw;
    if (!root || typeof root !== "object" || Array.isArray(root)) {
      pasteError = "Paste an mcpServers object, or the servers inside one.";
      return;
    }
    const found: McpServerRecord[] = [];
    for (const [key, value] of Object.entries(root as Record<string, Record<string, unknown>>)) {
      if (value && typeof value === "object") found.push(fromSnippet(key, value));
    }
    if (found.length === 0) {
      pasteError = "No server was found in that JSON.";
      return;
    }
    if (found.length === 1) {
      show(found[0]);
      return;
    }
    const names = new Set(found.map((s) => s.name));
    const saved = await saveMcp([...app.mcp.filter((s) => !names.has(s.name)).map(record), ...found]);
    if (!saved) return;
    const first = saved.find((s) => s.name === found[0].name);
    fresh = false;
    chosen = found[0].name;
    if (first) show(record(first));
  }

  function fromSnippet(key: string, value: Record<string, unknown>): McpServerRecord {
    const str = (v: unknown) => (typeof v === "string" ? v : "");
    const strMap = (v: unknown) =>
      v && typeof v === "object" && !Array.isArray(v)
        ? Object.fromEntries(Object.entries(v as Record<string, unknown>).map(([k, x]) => [k, String(x)]))
        : {};
    const type = str(value.type);
    const http = type === "http" || type === "streamable-http" || type === "streamableHttp" || (!value.command && !!value.url);
    return {
      name: key,
      enabled: value.enabled !== false,
      transport: http ? "http" : "stdio",
      command: str(value.command),
      args: Array.isArray(value.args) ? value.args.map(String) : [],
      env: strMap(value.env),
      cwd: str(value.cwd),
      url: str(value.url),
      headers: strMap(value.headers),
      timeout_s: typeof value.timeout_s === "number" ? value.timeout_s : 0,
      read_only: value.read_only === true,
      allow: Array.isArray(value.allow) ? value.allow.map(String) : [],
    };
  }

  function toggleAllow(tool: string) {
    if (!draft) return;
    const allow = draft.allow.includes(tool)
      ? draft.allow.filter((t) => t !== tool)
      : [...draft.allow, tool].sort();
    edit({ allow });
  }

  /** The tools to draw: what the last test listed, else what the server
      listed when it last ran. */
  const tools = $derived.by(() => {
    if (probe?.ok && probe.tools) return probe.tools;
    return (stored?.tools ?? []).map((t) => ({ name: t, description: "" }));
  });

  /** The word on a server's row, for the states worth reading at a glance. */
  function railWord(s: McpServer): string {
    if (!s.enabled) return "Off";
    if (s.problem) return "Cannot run";
    if (s.state === "running") return "Running";
    if (s.state === "failed") return "Failed";
    return "";
  }

  function stateLine(s: McpServer | null): string {
    if (!s) return "Not saved";
    if (!s.enabled) return "Off";
    if (s.problem) return "Cannot run";
    switch (s.state) {
      case "running":
        return s.running > 1 ? `Running in ${s.running} projects` : "Running";
      case "starting":
        return "Starting";
      case "failed":
        return "Failed";
    }
    return "Starts with the next turn";
  }
</script>

<SplitDialog label="MCP servers" title="MCP servers" onclose={leave} onback={onback ? back : undefined}>
  {#snippet rail()}
    <nav class="-mx-1 min-h-0 flex-1 overflow-y-auto px-1">
      {#each app.mcp as s (s.name)}
        <button class="rail-row" class:is-active={!fresh && s.name === stored?.name} onclick={() => select(s.name)}>
          <span class="min-w-0 flex-1 truncate" class:opacity-55={!s.enabled}>{s.name}</span>
          {#if railWord(s)}
            <span class="shrink-0 text-[11px] text-muted">{railWord(s)}</span>
          {/if}
        </button>
      {/each}
      {#if fresh}
        <button class="rail-row is-active">
          <span class="min-w-0 flex-1 truncate">{draft?.name || "New server"}</span>
          <span class="shrink-0 text-[11px] text-muted">Not saved</span>
        </button>
      {/if}
    </nav>

    <button class="rail-add" onclick={create}>
      <Plus size={14} />
      New server
    </button>
  {/snippet}

  {#snippet detail()}
    {#if draft}
      <header class="shrink-0 border-b border-hair px-6 pt-4 pr-14 pb-3.5">
        <div class="flex min-w-0 flex-col gap-0.5">
          <h3 class="truncate text-[17px] font-medium">{draft.name || "New server"}</h3>
          <span class="truncate text-[12px] text-muted">
            {stateLine(stored)}{(draft.transport === "http" ? draft.url : draft.command)
              ? ` · ${draft.transport === "http" ? draft.url : [draft.command, ...draft.args].join(" ")}`
              : ""}
          </span>
        </div>
      </header>

      <div bind:this={body} class="flex min-h-0 flex-1 flex-col gap-3.5 overflow-y-auto px-6 pt-4 pb-6">
        {#if stored?.problem || testing || probe || stored?.error}
          <div class="field-card gap-1.5">
            {#if stored?.problem}
              <p class="text-[12px] text-crit">{stored.problem}</p>
            {/if}
            {#if testing}
              <p class="flex items-center gap-2 text-[12px] text-muted">
                <span class="spinner"></span> Starting the server
              </p>
            {:else if probe?.ok}
              <p class="text-[12px] text-ok">
                Started {probe.server?.name || draft.name}{probe.server?.version ? " " + probe.server.version : ""},
                {probe.tools?.length ?? 0} tool{probe.tools?.length === 1 ? "" : "s"}.
              </p>
              {#each probe.notes ?? [] as note}
                <p class="text-[12px] text-muted">{note}</p>
              {/each}
            {:else if probe}
              <p class="text-[12px] text-crit">{probe.error}</p>
            {:else if stored?.error}
              <p class="text-[12px] text-crit">{stored.error}</p>
            {/if}
          </div>
        {/if}
        {#if fresh && !dirty}
          <div class="field-card">
            <div class="flex flex-col gap-0.5">
              <h4 class="field-head">Server JSON</h4>
              <span class="text-[12px] text-muted">
                The server's MCP configuration, including any tokens it needs.
              </span>
            </div>
            <textarea
              class="input mono"
              rows="4"
              placeholder={'{ "mcpServers": { "name": { "command": "…", "args": ["…"] } } }'}
              bind:value={paste}
              oninput={(e) => pasted(e.currentTarget.value)}
              spellcheck="false"
            ></textarea>
            {#if pasteError}
              <span class="text-[12px] text-crit">{pasteError}</span>
            {/if}
          </div>
        {/if}

        <div class="field-card">
          <label class="flex cursor-pointer items-center justify-between gap-6">
            <span class="flex min-w-0 flex-col gap-0.5">
              <span class="text-[13px]">Enabled</span>
              <span class="text-[12px] text-muted">A disabled server keeps its settings and offers no tools.</span>
            </span>
            <input
              class="switch"
              type="checkbox"
              checked={draft.enabled}
              onchange={(e) => edit({ enabled: e.currentTarget.checked })}
            />
          </label>

          <div class="grid grid-cols-2 gap-3">
            <label class="flex flex-col gap-1.5">
              <span class="label">Name</span>
              <input
                class="input"
                placeholder="blender"
                value={draft.name}
                oninput={(e) => edit({ name: e.currentTarget.value.trim() })}
                spellcheck="false"
                autocapitalize="off"
              />
              <span class="text-[11.5px] text-muted">Letters, digits, - and _. Tools are sent as mcp__name__tool.</span>
            </label>
            <div class="flex flex-col gap-1.5">
              <span class="label">Transport</span>
              <OptionPicker
                value={draft.transport}
                options={TRANSPORTS}
                onpick={(transport) => edit({ transport })}
                label="Transport"
              />
            </div>
          </div>

          {#if draft.transport === "http"}
            <label class="flex flex-col gap-1.5">
              <span class="label">Address</span>
              <input
                class="input num"
                placeholder="http://127.0.0.1:3845/mcp"
                bind:value={url}
                oninput={(e) => edit({ url: e.currentTarget.value.trim() })}
                spellcheck="false"
                autocapitalize="off"
              />
            </label>
            <label class="flex flex-col gap-1.5">
              <span class="label">Headers</span>
              <textarea
                class="input mono"
                rows="3"
                placeholder="Authorization: Bearer …"
                bind:value={headers}
                oninput={(e) => edit({ headers: parsePairs(e.currentTarget.value, ":") })}
                spellcheck="false"
              ></textarea>
              <span class="text-[11.5px] text-muted">One per line, as Name: value. Sent with every request.</span>
            </label>
          {:else}
            <label class="flex flex-col gap-1.5">
              <span class="label">Command</span>
              <input
                class="input mono"
                placeholder="uvx"
                bind:value={command}
                oninput={(e) => edit({ command: e.currentTarget.value.trim() })}
                spellcheck="false"
                autocapitalize="off"
              />
              <span class="text-[11.5px] text-muted">
                A full path is safest: a harness started as a service has a short PATH.
              </span>
            </label>
            <label class="flex flex-col gap-1.5">
              <span class="label">Arguments</span>
              <textarea
                class="input mono"
                rows="3"
                bind:value={args}
                oninput={(e) =>
                  edit({
                    args: e.currentTarget.value
                      .split("\n")
                      .map((a) => a.trim())
                      .filter((a) => a !== ""),
                  })}
                spellcheck="false"
              ></textarea>
              <span class="text-[11.5px] text-muted">One per line. {"{project}"} becomes the project's folder.</span>
            </label>
            <div class="grid grid-cols-2 gap-3">
              <label class="flex flex-col gap-1.5">
                <span class="label">Environment</span>
                <textarea
                  class="input mono"
                  rows="3"
                  placeholder="TOKEN=…"
                  bind:value={env}
                  oninput={(e) => edit({ env: parsePairs(e.currentTarget.value, "=") })}
                  spellcheck="false"
                ></textarea>
                <span class="text-[11.5px] text-muted">One per line, as NAME=value, added to the harness's own.</span>
              </label>
              <label class="flex flex-col gap-1.5">
                <span class="label">Working folder</span>
                <input
                  class="input mono"
                  placeholder="The harness's own"
                  bind:value={cwd}
                  oninput={(e) => edit({ cwd: e.currentTarget.value.trim() })}
                  spellcheck="false"
                  autocapitalize="off"
                />
                <span class="text-[11.5px] text-muted">
                  A server whose arguments, environment or folder name {"{project}"} runs once per project.
                </span>
              </label>
            </div>
          {/if}

          <div class="grid grid-cols-2 gap-3">
            <label class="flex flex-col gap-1.5">
              <span class="label">Timeout</span>
              <input
                class="input num"
                placeholder="Command timeout"
                bind:value={timeout}
                oninput={(e) => {
                  const n = Number.parseInt(e.currentTarget.value.trim(), 10);
                  edit({ timeout_s: Number.isNaN(n) ? 0 : Math.min(3600, Math.max(1, n)) });
                }}
              />
              <span class="text-[11.5px] text-muted">Seconds one tool call may take, 1 to 3600.</span>
            </label>
            <label class="flex cursor-pointer items-start justify-between gap-4 pt-5">
              <span class="flex min-w-0 flex-col gap-0.5">
                <span class="text-[13px]">Read only</span>
                <span class="text-[12px] text-muted">
                  You state that no tool here changes anything. Its tools then run in plan mode and beside other
                  reads.
                </span>
              </span>
              <input
                class="switch"
                type="checkbox"
                checked={draft.read_only}
                onchange={(e) => edit({ read_only: e.currentTarget.checked })}
              />
            </label>
          </div>

        </div>

        <div class="field-card">
          <div class="flex items-center justify-between gap-4">
            <h4 class="field-head">Tools</h4>
            <span class="text-[12px] text-muted">
              {draft.allow.length} of {tools.length || draft.allow.length} run without asking
            </span>
          </div>
          {#if tools.length === 0 && draft.allow.length === 0}
            <p class="text-[12px] text-muted">Press Test, or send a message, to list what this server offers.</p>
          {:else}
            <span class="text-[11.5px] text-muted">
              A highlighted tool runs without a card in allowlist mode. Ask mode still shows every call.
            </span>
            <div class="flex flex-wrap gap-2">
              {#each tools.length ? tools : draft.allow.map((t) => ({ name: t, description: "" })) as t (t.name)}
                <button
                  type="button"
                  class="mono cursor-pointer truncate rounded-full border px-3 py-1.5 text-left text-[11.5px] transition-colors"
                  class:border-brand={draft.allow.includes(t.name)}
                  class:bg-brand={draft.allow.includes(t.name)}
                  class:text-white={draft.allow.includes(t.name)}
                  class:border-line={!draft.allow.includes(t.name)}
                  class:bg-surface={!draft.allow.includes(t.name)}
                  class:text-ink-2={!draft.allow.includes(t.name)}
                  title={t.description || t.name}
                  onclick={() => toggleAllow(t.name)}
                >
                  {t.name}
                </button>
              {/each}
            </div>
          {/if}
        </div>
      </div>

      <footer class="flex shrink-0 items-center justify-end gap-2.5 border-t border-hair bg-panel px-6 py-3">
        <button class="btn btn-danger !border-transparent" onclick={remove}>{fresh ? "Discard" : "Delete"}</button>
        <button
          class="btn btn-ghost !border-transparent"
          disabled={testing}
          title="Starts the server as drawn, lists its tools and stops it"
          onclick={test}
        >
          {testing ? "Testing" : "Test"}
        </button>
        <button class="btn btn-primary" disabled={(!dirty && !fresh) || !draft.name || saving} onclick={save}>
          {saving ? "Saving" : "Save"}
        </button>
      </footer>
    {:else}
      <div class="grid flex-1 place-items-center px-6 text-center text-[13px] text-muted">
        No MCP servers. Add one to offer its tools to every agent.
      </div>
    {/if}
  {/snippet}
</SplitDialog>
