<script lang="ts">
  import EffortChip from "./EffortChip.svelte";
  import SamplingSlider from "./SamplingSlider.svelte";
  import AgentModelField from "./AgentModelField.svelte";
  import type { Agent, AgentPatch, Probe } from "../api";
  import { TEMPERATURE_STEPS, TOP_P_STEPS, probeAgent } from "../state/app.svelte";

  let {
    agent,
    onchange,
    compact = false,
  }: {
    agent: Agent;
    onchange: (patch: AgentPatch) => void;
    /** Leaves out what only means something once the agent is answering, which
        is what the first-run screen wants. */
    compact?: boolean;
  } = $props();

  // The drafts are filled from the agent below, and refilled when the panel is
  // pointed at another one. The key never comes back, so its field starts
  // empty whichever agent this is, and empty means the stored key is kept.
  let name = $state("");
  let url = $state("");
  let key = $state("");
  let instructions = $state("");

  let probe = $state<Probe | null>(null);
  let checking = $state(false);
  // The address a reading was taken at, so it is not taken twice for one.
  let probedAt = $state("");

  $effect(() => {
    name = agent.name;
    url = agent.url;
    key = "";
    instructions = agent.instructions;
    probe = null;
    probedAt = "";
  });

  $effect(() => {
    if (agent.url !== "" && agent.url !== probedAt) void look(false);
  });

  async function look(refresh: boolean) {
    const at = agent.url;
    if (at === "") return;
    probedAt = at;
    checking = true;
    const got = await probeAgent({ agent: agent.id, url: at }, refresh);
    checking = false;
    if (agent.url === at) probe = got;
  }

  const models = $derived(probe?.models ?? []);

  function commitName() {
    // A blank name is not a name, so the field goes back to the stored one.
    if (name.trim() === "") name = agent.name;
    else if (name.trim() !== agent.name) onchange({ name: name.trim() });
  }

  function commitUrl() {
    if (url.trim() !== agent.url) onchange({ url: url.trim() });
  }

  function commitKey() {
    if (key !== "") {
      onchange({ key });
      key = "";
    }
  }

  function commitInstructions() {
    if (instructions.trim() !== agent.instructions) onchange({ instructions: instructions.trim() });
  }

  /** Writes anything typed but not yet committed. Called before the panel is
      pointed at another agent or taken off the screen. */
  export function flush() {
    commitName();
    commitUrl();
    commitKey();
    commitInstructions();
  }
</script>

<div>
  <label class="label mb-1.5 block" for="agent-name">Name</label>
  <input
    id="agent-name"
    class="input"
    placeholder="Local llama.cpp"
    bind:value={name}
    onblur={commitName}
    onkeydown={(e) => e.key === "Enter" && e.currentTarget.blur()}
  />
</div>

<div>
  {#if !compact}
    <h4 class="mb-3 text-[13px] font-medium">Connection</h4>
  {/if}
  <label class="label mb-1.5 block" for="agent-url">Address</label>
  <input
    id="agent-url"
    class="input num"
    placeholder="http://127.0.0.1:8080"
    bind:value={url}
    onblur={commitUrl}
    onkeydown={(e) => e.key === "Enter" && e.currentTarget.blur()}
    spellcheck="false"
    autocapitalize="off"
  />

  <label class="label mt-4 mb-1.5 block" for="agent-key">API key</label>
  <div class="flex items-center gap-2">
    <input
      id="agent-key"
      class="input min-w-0 flex-1"
      type="password"
      placeholder={agent.key_set ? "Saved, type to replace" : "Optional"}
      bind:value={key}
      onblur={commitKey}
      onkeydown={(e) => e.key === "Enter" && e.currentTarget.blur()}
    />
    {#if agent.key_set}
      <button
        class="btn btn-sm btn-ghost shrink-0"
        onclick={() => {
          key = "";
          onchange({ key: null });
        }}
      >
        Remove key
      </button>
    {/if}
  </div>
  {#if agent.key_set}
    <p class="mt-1.5 text-[12px] text-muted">Leave empty to keep the saved key.</p>
  {/if}

  <label class="label mt-4 mb-1.5 block" for="agent-model">Model</label>
  <AgentModelField
    value={agent.model}
    {models}
    {probe}
    {checking}
    hasKey={agent.key_set}
    hasAddress={agent.url !== ""}
    onpick={(model) => onchange({ model })}
    onrefresh={() => void look(true)}
  />
</div>

{#if !compact}
  <div>
    <h4 class="mb-3 text-[13px] font-medium">Thinking</h4>
    <span class="label mb-1.5 block">Effort</span>
    <EffortChip value={agent.effort} onpick={(effort) => onchange({ effort })} />

    <label class="mt-4 flex cursor-pointer items-center gap-3">
      <span class="min-w-0 flex-1 text-[13px]">Preserve reasoning</span>
      <input
        class="switch"
        type="checkbox"
        checked={agent.preserve_reasoning}
        onchange={(e) => onchange({ preserve_reasoning: e.currentTarget.checked })}
      />
    </label>
    <p class="mt-1 text-[12px] text-muted">Keep thinking from earlier turns in the prompt.</p>
  </div>

  <div>
    <h4 class="mb-1 text-[13px] font-medium">Sampling</h4>
    <SamplingSlider
      label="Temperature"
      steps={TEMPERATURE_STEPS}
      value={agent.temperature}
      onpick={(temperature) => onchange({ temperature })}
    />
    <SamplingSlider
      label="Top p"
      steps={TOP_P_STEPS}
      value={agent.top_p}
      onpick={(top_p) => onchange({ top_p })}
    />
    <p class="mt-1 px-[9px] text-[12px] text-muted">
      Auto sends no field, so the model server's own value stands.
    </p>
  </div>

  <div>
    <h4 class="mb-3 text-[13px] font-medium">Extra instructions</h4>
    <textarea
      class="input h-[104px] resize-none leading-[1.55]"
      placeholder="Anything this agent should always be told"
      bind:value={instructions}
      onblur={commitInstructions}
    ></textarea>
    <p class="mt-1.5 text-[12px] text-muted">Added to the end of the built-in system prompt.</p>
  </div>
{/if}
