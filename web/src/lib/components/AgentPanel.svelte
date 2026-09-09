<script lang="ts">
  import SamplingSlider from "./SamplingSlider.svelte";
  import AgentModelField from "./AgentModelField.svelte";
  import type { Agent, AgentPatch, Effort, Probe, Protocol } from "../api";
  import {
    REPEAT_PENALTY_STEPS,
    TEMPERATURE_STEPS,
    TOP_K_STEPS,
    TOP_P_STEPS,
    probeAgent,
  } from "../state/app.svelte";
  import { effortHint, effortLabel, k, protocolHint, protocolLabel } from "../format";

  const EFFORTS: Effort[] = ["auto", "off", "low", "medium", "high"];
  const PROTOCOLS: Protocol[] = ["llamacpp", "chat", "responses"];

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
  // pointed at another one.
  let name = $state("");
  let url = $state("");
  let key = $state("");
  let instructions = $state("");
  let maxTokens = $state("");
  let seed = $state("");

  let probe = $state<Probe | null>(null);
  let checking = $state(false);
  // The address and key a reading was taken with, so one is not taken twice
  // for the same pair.
  let probedAt = $state("");
  // Which agent the drafts were filled from. The identity of the record
  // changes on every edit of an unsaved one, and that is not a new agent.
  let filled = $state<string | null>(null);

  $effect(() => {
    if (filled === agent.id) return;
    filled = agent.id;
    name = agent.name;
    url = agent.url;
    key = agent.key;
    instructions = agent.instructions;
    maxTokens = agent.max_tokens === null ? "" : String(agent.max_tokens);
    seed = agent.seed === null ? "" : String(agent.seed);
    probe = null;
    probedAt = "";
    void look(agent.url, agent.key, agent.protocol, false);
  });

  // The address is read with the key beside it, so an agent that has not been
  // stored yet is still read with the key the reader typed for it. A reading
  // is a call to the address, so it is taken when the panel is pointed at an
  // agent and when either field is left, never while one is being typed into.
  async function look(addr: string, token: string, how: Protocol, refresh: boolean) {
    if (addr === "") return;
    const asked = addr + " " + token + " " + how;
    if (!refresh && asked === probedAt) return;
    probedAt = asked;
    checking = true;
    const got = await probeAgent(
      { agent: agent.id, url: addr, key: token, protocol: how },
      refresh,
    );
    checking = false;
    if (asked === probedAt) probe = got;
  }

  const models = $derived(probe?.models ?? []);
  // The window the chosen model reported, so a cap is set against a number
  // rather than in the dark.
  const window = $derived(models.find((m) => m.id === agent.model)?.ctx ?? 0);

  // Every field commits as it is typed, so Save is lit by a change already
  // made rather than waiting for the field to be left. Each takes the text
  // rather than reading its own draft, so none of them depends on whether the
  // binding or the handler runs first.
  function commitName(text: string) {
    // A blank name is not a name, and is left uncommitted rather than written,
    // so the field has something to go back to when it is left.
    const next = text.trim();
    if (next !== "" && next !== agent.name) onchange({ name: next });
  }

  // A protocol is picked rather than typed, so the address is read again as
  // soon as it moves: what a llama.cpp address answers is not what the same
  // address answers as an OpenAI one.
  function pickProtocol(protocol: Protocol) {
    if (protocol === agent.protocol) return;
    onchange({ protocol });
    void look(url.trim(), key, protocol, false);
  }

  function restoreName() {
    if (name.trim() === "") name = agent.name;
  }

  function commitUrl(text: string) {
    if (text.trim() !== agent.url) onchange({ url: text.trim() });
  }

  function commitKey(text: string) {
    if (text !== agent.key) onchange({ key: text });
  }

  function commitInstructions(text: string) {
    if (text.trim() !== agent.instructions) onchange({ instructions: text.trim() });
  }

  /** A count field, where anything that is not a number is no value at all. */
  function commitCount(text: string, current: number | null, apply: (v: number | null) => void) {
    const n = Number.parseInt(text.trim(), 10);
    const next = text.trim() === "" || Number.isNaN(n) ? null : n;
    if (next !== current) apply(next);
  }

  function commitMaxTokens(text: string) {
    commitCount(text, agent.max_tokens, (max_tokens) => onchange({ max_tokens }));
  }

  function commitSeed(text: string) {
    commitCount(text, agent.seed, (v) => onchange({ seed: v }));
  }

  /** Writes anything typed but not yet committed. Called before the panel is
      pointed at another agent or taken off the screen. */
  export function flush() {
    restoreName();
    commitName(name);
    commitUrl(url);
    commitKey(key);
    commitInstructions(instructions);
    commitMaxTokens(maxTokens);
    commitSeed(seed);
  }
</script>

{#if compact}
  <div>
    <label class="label mb-1.5 block" for="agent-name">Name</label>
    <input
      id="agent-name"
      class="input"
      placeholder="Local llama.cpp"
      bind:value={name}
      oninput={(e) => commitName(e.currentTarget.value)}
      onblur={restoreName}
    />
  </div>

  <div>
    <label class="label mb-1.5 block" for="agent-url">Address</label>
    <input
      id="agent-url"
      class="input num"
      placeholder="http://127.0.0.1:8080"
      bind:value={url}
      oninput={(e) => commitUrl(e.currentTarget.value)}
      onblur={() => void look(url.trim(), key, agent.protocol, false)}
      onkeydown={(e) => e.key === "Enter" && e.currentTarget.blur()}
      spellcheck="false"
      autocapitalize="off"
    />

    <label class="label mt-4 mb-1.5 block" for="agent-key">API key</label>
    <input
      id="agent-key"
      class="input"
      type="password"
      placeholder="Leave empty if the address takes none"
      bind:value={key}
      oninput={(e) => commitKey(e.currentTarget.value)}
      onblur={() => void look(url.trim(), key, agent.protocol, false)}
      onkeydown={(e) => e.key === "Enter" && e.currentTarget.blur()}
    />

    <span class="label mt-4 mb-1.5 block">Protocol</span>
    <div class="seg w-fit" role="group" aria-label="Protocol">
      {#each PROTOCOLS as level (level)}
        <button
          class:is-active={agent.protocol === level}
          aria-pressed={agent.protocol === level}
          onclick={() => pickProtocol(level)}
        >
          {protocolLabel(level)}
        </button>
      {/each}
    </div>
    <p class="mt-1.5 text-[12px] text-muted">{protocolHint(agent.protocol)}</p>

    <span class="label mt-4 mb-1.5 block">Model</span>
    <AgentModelField
      value={agent.model}
      {models}
      {probe}
      {checking}
      hasKey={agent.key !== ""}
      hasAddress={agent.url !== ""}
      onpick={(model) => onchange({ model })}
      onrefresh={() => void look(url.trim(), key, agent.protocol, true)}
    />
  </div>
{:else}
  <div class="field-card">
    <h4 class="field-head">Connection</h4>
    <label class="flex flex-col gap-1.5">
      <span class="label">Name</span>
      <input
        class="input"
        placeholder="Local llama.cpp"
        bind:value={name}
        oninput={(e) => commitName(e.currentTarget.value)}
        onblur={restoreName}
      />
    </label>
    <div class="grid grid-cols-2 gap-3">
      <label class="flex flex-col gap-1.5">
        <span class="label">Address</span>
        <input
          class="input num"
          placeholder="http://127.0.0.1:8080"
          bind:value={url}
          oninput={(e) => commitUrl(e.currentTarget.value)}
          onblur={() => void look(url.trim(), key, agent.protocol, false)}
          onkeydown={(e) => e.key === "Enter" && e.currentTarget.blur()}
          spellcheck="false"
          autocapitalize="off"
        />
      </label>
      <label class="flex flex-col gap-1.5">
        <span class="label">API key</span>
        <input
          class="input"
          type="password"
          placeholder="Leave empty if the address takes none"
          bind:value={key}
          oninput={(e) => commitKey(e.currentTarget.value)}
          onblur={() => void look(url.trim(), key, agent.protocol, false)}
          onkeydown={(e) => e.key === "Enter" && e.currentTarget.blur()}
        />
      </label>
    </div>
    <div class="flex flex-col gap-1.5">
      <span class="label">Protocol</span>
      <div class="seg self-start" role="group" aria-label="Protocol">
        {#each PROTOCOLS as level (level)}
          <button
            class:is-active={agent.protocol === level}
            aria-pressed={agent.protocol === level}
            onclick={() => pickProtocol(level)}
          >
            {protocolLabel(level)}
          </button>
        {/each}
      </div>
      <p class="text-[12px] text-muted">{protocolHint(agent.protocol)}</p>
    </div>
    <div class="flex flex-col gap-1.5">
      <span class="label">Model</span>
      <AgentModelField
        value={agent.model}
        {models}
        {probe}
        {checking}
        hasKey={agent.key !== ""}
        hasAddress={agent.url !== ""}
        onpick={(model) => onchange({ model })}
        onrefresh={() => void look(url.trim(), key, agent.protocol, true)}
      />
    </div>
  </div>

  <div class="field-card">
    <div class="flex items-start justify-between gap-4">
      <div class="flex min-w-0 flex-col gap-0.5">
        <h4 class="field-head">Thinking</h4>
        <span class="text-[12px] text-muted">{effortHint(agent.effort)}</span>
      </div>
      <div class="seg shrink-0" role="group" aria-label="Effort">
        {#each EFFORTS as level (level)}
          <button
            class:is-active={agent.effort === level}
            aria-pressed={agent.effort === level}
            onclick={() => onchange({ effort: level })}
          >
            {effortLabel(level)}
          </button>
        {/each}
      </div>
    </div>
    <label class="flex cursor-pointer items-center justify-between gap-6 border-t border-hair pt-3">
      <span class="flex min-w-0 flex-col gap-0.5">
        <span class="text-[13px]">Preserve reasoning</span>
        <span class="text-[12px] text-muted">Keep thinking from earlier turns in the prompt.</span>
      </span>
      <input
        class="switch"
        type="checkbox"
        checked={agent.preserve_reasoning}
        onchange={(e) => onchange({ preserve_reasoning: e.currentTarget.checked })}
      />
    </label>
  </div>

  <div class="flex flex-col gap-3">
    <div class="flex items-center justify-between gap-3">
      <h4 class="field-head">Sampling</h4>
      <span class="text-[12px] text-muted">Auto leaves the value to the model server</span>
    </div>
    <div class="grid grid-cols-2 gap-3">
      <SamplingSlider
        label="Temperature"
        hint="Lower is steadier, higher is more inventive."
        steps={TEMPERATURE_STEPS}
        value={agent.temperature}
        onpick={(temperature) => onchange({ temperature })}
      />
      <SamplingSlider
        label="Top p"
        hint="Nucleus cutoff for candidate tokens."
        steps={TOP_P_STEPS}
        value={agent.top_p}
        onpick={(top_p) => onchange({ top_p })}
      />
      <SamplingSlider
        label="Top k"
        hint="Candidates per step. Zero is no cap. Only where the engine takes it."
        steps={TOP_K_STEPS}
        value={agent.top_k}
        onpick={(top_k) => onchange({ top_k })}
      />
      <SamplingSlider
        label="Repeat penalty"
        hint="Discourages verbatim loops. Only where the engine takes it."
        steps={REPEAT_PENALTY_STEPS}
        value={agent.repeat_penalty}
        onpick={(repeat_penalty) => onchange({ repeat_penalty })}
      />
    </div>
    <div class="field-card !flex-row !items-center !gap-3 !py-3">
      <span class="shrink-0 text-[13px]">Max output tokens</span>
      <input
        class="input num w-[96px] shrink-0 !px-2.5 !py-1.5"
        placeholder="Auto"
        bind:value={maxTokens}
        oninput={(e) => commitMaxTokens(e.currentTarget.value)}
      />
      {#if window > 0}
        <span class="shrink-0 text-[12px] text-muted">of {k(window)} context</span>
      {/if}
      <span class="flex-1"></span>
      <span class="shrink-0 text-[13px]">Seed</span>
      <input
        class="input num w-[96px] shrink-0 !px-2.5 !py-1.5"
        placeholder="Random"
        bind:value={seed}
        oninput={(e) => commitSeed(e.currentTarget.value)}
      />
    </div>
  </div>

  <div class="field-card !gap-2.5">
    <div class="flex items-center justify-between gap-3">
      <h4 class="field-head">Extra instructions</h4>
      <span class="num text-[12px] text-muted">{instructions.length} / 4000</span>
    </div>
    <p class="-mt-1 text-[12px] text-muted">Added to the end of the built-in system prompt.</p>
    <textarea
      class="input h-[112px] resize-none leading-[1.6]"
      maxlength="4000"
      placeholder="Anything this agent should always be told"
      bind:value={instructions}
      oninput={(e) => commitInstructions(e.currentTarget.value)}
    ></textarea>
  </div>
{/if}
