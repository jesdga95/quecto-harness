<script lang="ts">
  import X from "phosphor-svelte/lib/X";
  import { app, removeAllowed, saveConfig } from "../state/app.svelte";

  let { onclose }: { onclose: () => void } = $props();

  const config = app.config;
  let url = $state(config?.server.url ?? "");
  let token = $state(config?.server.token ?? "");
  let listen = $state(config?.listen ?? "");
  let compactAt = $state(String(config?.compact_at ?? 0.8));
  let shell = $state(config?.shell ?? "");
  let timeout = $state(String(config?.command_timeout_s ?? 120));

  function save() {
    void saveConfig({
      server: { url: url.trim(), token: token.trim() },
      listen: listen.trim(),
      compact_at: Number(compactAt) || 0.8,
      shell: shell.trim(),
      command_timeout_s: Number(timeout) || 120,
    });
  }
</script>

<div
  class="fixed inset-0 z-50 grid place-items-center bg-[rgb(15_23_42/0.42)] p-6"
  role="presentation"
  onclick={(e) => e.target === e.currentTarget && onclose()}
>
  <div class="card w-[540px] max-w-full overflow-hidden" role="dialog" aria-label="Settings">
    <div class="flex items-center justify-between px-5 pt-4">
      <h2 class="text-[15px] font-medium">Settings</h2>
      <button class="icon-btn" aria-label="Close" onclick={onclose}><X /></button>
    </div>

    <div class="grid max-h-[62vh] grid-cols-2 gap-4 overflow-y-auto px-5 py-4">
      <label class="col-span-2 flex flex-col gap-1.5">
        <span class="label">Server url</span>
        <input class="input" bind:value={url} placeholder="Your llama.cpp server address" />
      </label>
      <label class="flex flex-col gap-1.5">
        <span class="label">Token</span>
        <input class="input" type="password" bind:value={token} placeholder="Empty for none" />
      </label>
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
  </div>
</div>
