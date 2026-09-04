<script lang="ts">
  import Logo from "./Logo.svelte";
  import { app, connectServer } from "../state/app.svelte";
  import { sentence } from "../format";

  let { ondone }: { ondone: () => void } = $props();

  let url = $state(app.config?.server.url ?? "");
  let token = $state(app.config?.server.token ?? "");
  let problem = $state("");

  // Probe, and go in when something answered. A server that refuses the key
  // still answered, so the key is the user's to get right, not a gate here.
  async function connect() {
    if (!url.trim() || app.probing) return;
    problem = "";
    const err = await connectServer(url.trim(), token.trim());
    problem = err;
    if (err === "") ondone();
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
        <div class="field-lg">
          <input
            id="ob-url"
            class="num flex-1 border-0 bg-transparent p-0 text-[15px] outline-none"
            placeholder="Your llama.cpp server address"
            bind:value={url}
            onkeydown={(e) => e.key === "Enter" && connect()}
            spellcheck="false"
            autocapitalize="off"
          />
        </div>
        {#if problem}
          <p class="mt-2.5 text-[13px] text-crit">{sentence(problem)}</p>
        {/if}
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

      <div class="mt-9">
        <button class="btn-lg" onclick={connect} disabled={app.probing || !url.trim()}>
          {app.probing ? "Checking" : "Connect"}
        </button>
      </div>

      <p class="mt-8 text-[12px] text-muted">
        Saved to <span class="num">~/.quecto-harness/config.json</span>, changeable in
        settings.
      </p>
    </div>
  </main>
</div>
