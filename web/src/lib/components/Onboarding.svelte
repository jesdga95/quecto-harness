<script lang="ts">
  import Logo from "./Logo.svelte";
  import StatusDot from "./StatusDot.svelte";
  import { app, connectServer } from "../state/app.svelte";
  import { k } from "../format";

  let { ondone }: { ondone: () => void } = $props();

  let url = $state(app.config?.server.url ?? "");
  let token = $state(app.config?.server.token ?? "");
  let problem = $state("");
  let found = $state(false);
  let took = $state(0);

  const reachable = $derived(found && problem === "");

  const statusLine = $derived(
    problem !== ""
      ? problem
      : found
        ? `${app.mode === "router" ? "Router mode" : "Single model mode"}, ${app.models.length} ${
            app.models.length === 1 ? "model" : "models"
          }.`
        : "Not connected yet.",
  );

  async function connect() {
    if (!url.trim() || app.probing) return;
    problem = "";
    found = false;
    const started = performance.now();
    const err = await connectServer(url.trim(), token.trim());
    took = Math.round(performance.now() - started);
    if (err) problem = err;
    else found = true;
  }
</script>

<div class="onboard">
  <aside class="onboard-panel">
    <div class="onboard-mark">
      <Logo size={168} wordmark={false} panel />
    </div>
    <h1 class="onboard-title">Quecto Harness</h1>
    <p class="onboard-copy">
      A small agent that reads, writes and runs commands in one project, using a model you
      host.
    </p>
    <p class="onboard-foot">Only talks to the server you point it at.</p>
  </aside>

  <main class="onboard-form">
    <div class="w-full max-w-[460px]">
      <h2 class="text-[27px] leading-tight font-medium tracking-[-0.015em]">
        Connect your server
      </h2>

      <div class="mt-9">
        <label class="label mb-2.5 block" for="ob-url">Address</label>
        <div class="field-lg" class:is-live={reachable}>
          <input
            id="ob-url"
            class="num flex-1 border-0 bg-transparent p-0 text-[15px] outline-none"
            placeholder="Your llama.cpp server address"
            bind:value={url}
            onkeydown={(e) => e.key === "Enter" && connect()}
            spellcheck="false"
            autocapitalize="off"
          />
          <span class="flex items-center gap-2 text-[12px]" class:text-accent={reachable}>
            <span class="dot" class:dot-loaded={reachable}></span>
            <span class="num">{found ? `${took}ms` : "—"}</span>
          </span>
        </div>
        <p class="mt-2.5 text-[13px]" class:text-crit={problem !== ""} class:text-ink-2={problem === ""}>
          {statusLine}
        </p>
      </div>

      <div class="mt-7">
        <label class="label mb-2.5 block" for="ob-token">API key</label>
        <div class="field-lg">
          <input
            id="ob-token"
            class="flex-1 border-0 bg-transparent p-0 text-[15px] outline-none"
            type="password"
            placeholder="Optional"
            bind:value={token}
            onkeydown={(e) => e.key === "Enter" && connect()}
          />
        </div>
      </div>

      {#if found}
        <div class="mt-8 max-h-[220px] overflow-y-auto pr-1">
          {#each app.models as model (model.id)}
            <div class="flex items-center gap-2.5 py-[7px] text-[13.5px]">
              <StatusDot status={model.status} />
              <span class="min-w-0 flex-1 truncate">{model.id}</span>
              <span class="num text-[12px] text-muted">{k(model.ctx)}</span>
            </div>
          {/each}
        </div>
      {/if}

      <div class="mt-9">
        {#if found}
          <button class="btn-lg" onclick={ondone}>Start</button>
        {:else}
          <button class="btn-lg" onclick={connect} disabled={app.probing || !url.trim()}>
            {app.probing ? "Checking" : "Connect"}
          </button>
        {/if}
      </div>

      <p class="mt-8 text-[12px] text-muted">
        Saved to <span class="num">~/.quecto-harness/config.json</span>, changeable in
        settings.
      </p>
    </div>
  </main>
</div>
