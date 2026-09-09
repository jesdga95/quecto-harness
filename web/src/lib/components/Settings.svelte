<script lang="ts">
  import X from "phosphor-svelte/lib/X";
  import Modal from "./Modal.svelte";
  import { activeAgent, app, removeAllowed, saveConfig } from "../state/app.svelte";

  let { onclose }: { onclose: () => void } = $props();

  const config = app.config;
  let listen = $state(config?.listen ?? "");
  let compactAt = $state(String(config?.compact_at ?? 0.8));
  let shell = $state(config?.shell ?? "");
  let timeout = $state(String(config?.command_timeout_s ?? 120));

  const answering = $derived(activeAgent());

  function save() {
    void saveConfig({
      listen: listen.trim(),
      compact_at: Number(compactAt) || 0.8,
      shell: shell.trim(),
      command_timeout_s: Number(timeout) || 120,
    });
  }

  function manage() {
    onclose();
    app.agentsOpen = true;
  }
</script>

<Modal title="Settings" onclose={onclose}>
  <div class="grid max-h-[62vh] grid-cols-2 gap-4 overflow-y-auto px-5 py-4">
    <div class="col-span-2 flex flex-col gap-1.5">
      <span class="label">Agents</span>
      <div class="flex items-center gap-3">
        <span class="min-w-0 flex-1 text-[12.5px] text-muted">
          {answering ? `${answering.name} is answering.` : "No agent yet."}
        </span>
        <button class="btn btn-sm btn-ghost shrink-0" onclick={manage}>Manage agents</button>
      </div>
    </div>

    <label class="flex flex-col gap-1.5">
      <span class="label">Listen address</span>
      <input class="input" bind:value={listen} />
    </label>
    <label class="flex flex-col gap-1.5">
      <span class="label">Compact at</span>
      <input class="input num" bind:value={compactAt} />
      <span class="text-[11.5px] text-muted">Fraction of the context window, 0 to 1</span>
    </label>
    <label class="flex flex-col gap-1.5">
      <span class="label">Shell</span>
      <input class="input" bind:value={shell} />
    </label>
    <label class="flex flex-col gap-1.5">
      <span class="label">Command timeout</span>
      <input class="input num" bind:value={timeout} />
      <span class="text-[11.5px] text-muted">Seconds</span>
    </label>

    <div class="col-span-2 flex flex-col gap-2">
      <span class="label">Allowlist for this project</span>
      {#each app.allowlist as prefix (prefix)}
        <div class="flex items-center gap-2">
          <span class="mono-block flex-1 !py-1.5">{prefix}</span>
          <button class="icon-btn" aria-label="Remove {prefix}" onclick={() => removeAllowed(prefix)}>
            <X />
          </button>
        </div>
      {:else}
        <p class="text-[12.5px] text-muted">Nothing allowed yet.</p>
      {/each}
    </div>
  </div>

  <div class="flex items-center justify-end gap-3 px-5 pt-1 pb-4">
    <button class="btn btn-primary" onclick={save}>Save</button>
    <button class="btn btn-ghost" onclick={onclose}>Close</button>
  </div>
</Modal>
