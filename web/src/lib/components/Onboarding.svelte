<script lang="ts">
  import Logo from "./Logo.svelte";
  import AgentPanel from "./AgentPanel.svelte";
  import type { Agent, AgentPatch } from "../api";
  import { addAgent, app } from "../state/app.svelte";

  // The draft the panel edits before anything is stored. It is the same panel
  // the dialog uses, so the two forms cannot drift.
  let draft = $state<Agent>({
    id: "",
    name: "",
    url: "",
    key_set: false,
    model: "",
    effort: "medium",
    preserve_reasoning: true,
    temperature: null,
    top_p: null,
    instructions: "",
  });
  let key = $state("");
  let panel = $state<ReturnType<typeof AgentPanel> | null>(null);
  let problem = $state("");
  let saving = $state(false);

  function edit(patch: AgentPatch) {
    if (patch.key !== undefined) {
      key = patch.key ?? "";
      draft = { ...draft, key_set: key !== "" };
      return;
    }
    draft = { ...draft, ...patch };
  }

  async function create() {
    panel?.flush();
    if (draft.name.trim() === "" || saving) return;
    saving = true;
    problem = "";
    const made = await addAgent({
      name: draft.name.trim(),
      url: draft.url.trim(),
      key,
      model: draft.model.trim(),
    });
    saving = false;
    if (!made) problem = app.error || "The harness did not accept that agent";
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
    <p class="onboard-foot">Only talks to the servers you point it at.</p>
  </aside>

  <main class="onboard-form">
    <div class="w-full max-w-[460px]">
      <h2 class="text-[27px] leading-tight font-medium tracking-[-0.015em]">
        Create your first agent
      </h2>

      <div class="mt-9 space-y-5">
        <AgentPanel bind:this={panel} agent={draft} onchange={edit} compact />
      </div>

      {#if problem}
        <p class="mt-2.5 text-[13px] text-crit">{problem}</p>
      {/if}

      <div class="mt-9">
        <button class="btn-lg" onclick={create} disabled={saving || draft.name.trim() === ""}>
          {saving ? "Saving" : "Create agent"}
        </button>
      </div>

      <p class="mt-8 text-[12px] text-muted">
        Saved to <span class="num">~/.quecto-harness/config.json</span>, changeable in the
        agents dialog.
      </p>
    </div>
  </main>
</div>
