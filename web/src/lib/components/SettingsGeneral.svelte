<script lang="ts">
  import { untrack } from "svelte";
  import type { Config } from "../api";
  import { app } from "../state/app.svelte";

  let {
    config,
    onchange,
    onmanage,
  }: {
    config: Config;
    onchange: (patch: Partial<Config>) => void;
    onmanage: () => void;
  } = $props();

  const answering = $derived(app.agents.find((a) => a.id === config.agent));

  let listen = $state("");
  let compactAt = $state("0.8");
  let shell = $state("");
  let timeout = $state("120");
  let after = $state("30");
  let limit = $state("16384");
  let maxCalls = $state("50");
  let editing: "compactAt" | "timeout" | "after" | "limit" | "maxCalls" | null = $state(null);

  // The pane is remounted for each visit, so the fill is one-way, from the
  // draft it is pointed at. Typing commits upward; a commit changing the
  // draft must not refill the field the user has the caret in, and the refill
  // runs only while the user is not in one, so a value that does not pass
  // validation in one field is not discarded by a commit landing in another.
  $effect(() => {
    const source = untrack(() => config);
    listen = source.listen;
    if (editing !== "compactAt") compactAt = String(source.compact_at);
    shell = source.shell;
    if (editing !== "timeout") timeout = String(source.command_timeout_s);
    if (editing !== "after") after = String(source.background_after_s);
    if (editing !== "limit") limit = String(source.output_limit_bytes);
    if (editing !== "maxCalls") maxCalls = String(source.max_tool_calls);
  });

  function commitListen(text: string) {
    if (text.trim() !== config.listen) onchange({ listen: text.trim() });
  }

  function commitShell(text: string) {
    if (text.trim() !== config.shell) onchange({ shell: text.trim() });
  }

  function commitCompact(text: string) {
    const v = Number.parseFloat(text.trim());
    if (Number.isNaN(v) || v < 0.05 || v > 0.95) return;
    if (v !== config.compact_at) onchange({ compact_at: v });
  }

  function commitTimeout(text: string) {
    const n = Number.parseInt(text.trim(), 10);
    if (Number.isNaN(n)) return;
    const next = Math.min(3600, Math.max(1, n));
    if (next !== config.command_timeout_s) onchange({ command_timeout_s: next });
  }

  function commitAfter(text: string) {
    const n = Number.parseInt(text.trim(), 10);
    if (Number.isNaN(n)) return;
    const next = Math.min(3600, Math.max(0, n));
    if (next !== config.background_after_s) onchange({ background_after_s: next });
  }

  function commitLimit(text: string) {
    const n = Number.parseInt(text.trim(), 10);
    if (Number.isNaN(n)) return;
    const next = Math.min(4194304, Math.max(1024, n));
    if (next !== config.output_limit_bytes) onchange({ output_limit_bytes: next });
  }

  function commitMaxCalls(text: string) {
    const n = Number.parseInt(text.trim(), 10);
    if (Number.isNaN(n)) return;
    const next = Math.min(1000, Math.max(0, n));
    if (next !== config.max_tool_calls) onchange({ max_tool_calls: next });
  }

  function invalid(text: string, min: number, max: number): boolean {
    const v = Number(text.trim());
    return text.trim() === "" || Number.isNaN(v) || v < min || v > max;
  }

  /** Writes anything typed but not yet committed. Called before the pane is
      pointed at another category or taken off the screen. */
  export function flush() {
    if (invalid(compactAt, 0.05, 0.95)) compactAt = String(config.compact_at);
    if (invalid(timeout, 1, 3600)) timeout = String(config.command_timeout_s);
    if (invalid(after, 0, 3600)) after = String(config.background_after_s);
    if (invalid(limit, 1024, 4194304)) limit = String(config.output_limit_bytes);
    if (invalid(maxCalls, 0, 1000)) maxCalls = String(config.max_tool_calls);
    commitListen(listen);
    commitCompact(compactAt);
    commitShell(shell);
    commitTimeout(timeout);
    commitAfter(after);
    commitLimit(limit);
    commitMaxCalls(maxCalls);
  }
</script>

<div class="field-card">
  <div class="flex items-center justify-between gap-4">
    <div class="flex min-w-0 flex-col gap-0.5">
      <h4 class="field-head">Agents</h4>
      <span class="text-[12px] text-muted">
        {answering ? `${answering.name} is answering.` : "No agent yet."}
      </span>
    </div>
    <button class="btn btn-sm btn-ghost shrink-0" onclick={onmanage}>Manage agents</button>
  </div>
</div>

<div class="field-card">
  <h4 class="field-head">Connection</h4>
  <div class="grid grid-cols-2 gap-3">
    <label class="flex flex-col gap-1.5">
      <span class="label">Listen address</span>
      <input
        class="input"
        bind:value={listen}
        oninput={(e) => commitListen(e.currentTarget.value)}
      />
      <span class="text-[11.5px] text-muted">Changes take effect on restart.</span>
    </label>
    <label class="flex flex-col gap-1.5">
      <span class="label">Shell</span>
      <input class="input" bind:value={shell} oninput={(e) => commitShell(e.currentTarget.value)} />
    </label>
  </div>
</div>

<div class="field-card">
  <h4 class="field-head">Commands</h4>
  <div class="grid grid-cols-2 gap-3">
    <label class="flex flex-col gap-1.5">
      <span class="label">Command timeout</span>
      <input
        class="input num"
        bind:value={timeout}
        oninput={(e) => commitTimeout(e.currentTarget.value)}
        onfocus={() => (editing = "timeout")}
        onblur={() => (editing = null)}
      />
      <span class="text-[11.5px] text-muted">Seconds, 1 to 3600</span>
    </label>
    <label class="flex flex-col gap-1.5">
      <span class="label">Background after</span>
      <input
        class="input num"
        bind:value={after}
        oninput={(e) => commitAfter(e.currentTarget.value)}
        onfocus={() => (editing = "after")}
        onblur={() => (editing = null)}
      />
      <span class="text-[11.5px] text-muted">Seconds a command is waited on before it moves to the background, 0 for never</span>
    </label>
    <label class="flex flex-col gap-1.5">
      <span class="label">Output limit</span>
      <input
        class="input num"
        bind:value={limit}
        oninput={(e) => commitLimit(e.currentTarget.value)}
        onfocus={() => (editing = "limit")}
        onblur={() => (editing = null)}
      />
      <span class="text-[11.5px] text-muted">Bytes a tool result may be, 1024 to 4194304</span>
    </label>
  </div>
</div>

<div class="field-card">
  <h4 class="field-head">Context</h4>
  <div class="grid grid-cols-2 gap-3">
    <label class="flex flex-col gap-1.5">
      <span class="label">Compact at</span>
      <input
        class="input num"
        bind:value={compactAt}
        oninput={(e) => commitCompact(e.currentTarget.value)}
        onfocus={() => (editing = "compactAt")}
        onblur={() => (editing = null)}
      />
      <span class="text-[11.5px] text-muted">Fraction of the context window, 0.05 to 0.95</span>
    </label>
    <label class="flex flex-col gap-1.5">
      <span class="label">Max tool calls</span>
      <input
        class="input num"
        bind:value={maxCalls}
        oninput={(e) => commitMaxCalls(e.currentTarget.value)}
        onfocus={() => (editing = "maxCalls")}
        onblur={() => (editing = null)}
      />
      <span class="text-[11.5px] text-muted">Tool calls one turn may make. 0 is no limit.</span>
    </label>
  </div>
</div>
