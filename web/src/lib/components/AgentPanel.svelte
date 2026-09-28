<script lang="ts">
  import OptionPicker from "./OptionPicker.svelte";
  import SamplingSlider from "./SamplingSlider.svelte";
  import AgentModelField from "./AgentModelField.svelte";
  import type { Agent, AgentPatch, Effort, Probe, Protocol } from "../api";
  import {
    MAX_CONCURRENCY,
    MAX_DESCRIPTION,
    REPEAT_PENALTY_STEPS,
    TEMPERATURE_STEPS,
    TOP_K_STEPS,
    TOP_P_STEPS,
    app,
    probeAgent,
  } from "../state/app.svelte";
  import { effortHint, effortLabel, k, protocolHint, protocolLabel } from "../format";

  const EFFORTS = (["auto", "off", "low", "medium", "high", "xhigh"] as Effort[]).map((value) => ({
    value,
    label: effortLabel(value),
    hint: effortHint(value),
  }));
  const PROTOCOLS = (["llamacpp", "chat", "responses"] as Protocol[]).map((value) => ({
    value,
    label: protocolLabel(value),
    hint: protocolHint(value),
  }));

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
  let description = $state("");
  let instructions = $state("");
  let maxTokens = $state("");
  let maxContext = $state("");
  let seed = $state("");
  let concurrency = $state("");

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
    description = agent.description;
    instructions = agent.instructions;
    maxTokens = agent.max_tokens === null ? "" : String(agent.max_tokens);
    maxContext = agent.max_context === null ? "" : String(agent.max_context);
    seed = agent.seed === null ? "" : String(agent.seed);
    concurrency = String(agent.concurrency);
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
  // What the model server said the chosen model holds, which is what the
  // context field takes when the agent names no window of its own.
  const reported = $derived(models.find((m) => m.id === agent.model)?.ctx ?? 0);

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

  // The list holds ids, and the panel is the only thing that writes it, so a
  // chip flips its own entry rather than rebuilding the whole list.
  function toggleCall(id: string) {
    const calls = agent.calls.includes(id)
      ? agent.calls.filter((x) => x !== id)
      : [...agent.calls, id];
    onchange({ calls });
  }

  function commitUrl(text: string) {
    if (text.trim() !== agent.url) onchange({ url: text.trim() });
  }

  function commitKey(text: string) {
    if (text !== agent.key) onchange({ key: text });
  }

  function commitDescription(text: string) {
    if (text.trim() !== agent.description) onchange({ description: text.trim() });
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

  function commitMaxContext(text: string) {
    commitCount(text, agent.max_context, (max_context) => onchange({ max_context }));
  }

  function commitSeed(text: string) {
    commitCount(text, agent.seed, (v) => onchange({ seed: v }));
  }

  // Concurrency always has a value, so an unreadable draft is not a null but
  // nothing at all, and a number out of range is pulled into it here rather
  // than sent to be refused.
  function commitConcurrency(text: string) {
    const n = Number.parseInt(text.trim(), 10);
    if (Number.isNaN(n)) return;
    const next = Math.min(MAX_CONCURRENCY, Math.max(1, n));
    if (next !== agent.concurrency) onchange({ concurrency: next });
  }

  function restoreConcurrency() {
    concurrency = String(agent.concurrency);
  }

  /** Writes anything typed but not yet committed. Called before the panel is
      pointed at another agent or taken off the screen. */
  export function flush() {
    restoreName();
    commitName(name);
    commitUrl(url);
    commitKey(key);
    commitDescription(description);
    commitInstructions(instructions);
    commitMaxTokens(maxTokens);
    commitMaxContext(maxContext);
    commitSeed(seed);
    commitConcurrency(concurrency);
    restoreConcurrency();
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
    <OptionPicker
      value={agent.protocol}
      options={PROTOCOLS}
      onpick={pickProtocol}
      label="Protocol"
    />

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
    <label class="flex cursor-pointer items-center justify-between gap-6">
      <span class="flex min-w-0 flex-col gap-0.5">
        <span class="text-[13px]">Enabled</span>
        <span class="text-[12px] text-muted">
          A disabled agent keeps its settings but does not answer or take work.
        </span>
      </span>
      <input
        class="switch"
        type="checkbox"
        checked={agent.enabled}
        onchange={(e) => onchange({ enabled: e.currentTarget.checked })}
      />
    </label>
  </div>

  <div class="field-card" class:opacity-45={!agent.enabled} class:pointer-events-none={!agent.enabled}>
    <div class="flex items-center justify-between gap-4">
      <h4 class="field-head">Delegation</h4>
      {#if agent.calls.length}
        <span class="text-[12px] text-muted">
          Can hand work to {agent.calls.length} agent{agent.calls.length === 1 ? "" : "s"}
        </span>
      {:else}
        <span class="text-[12px] text-crit">Answers the person only</span>
      {/if}
    </div>
    <div class="grid grid-cols-4 gap-2">
      {#each app.agents as other (other.id)}
        <button
          type="button"
          class="cursor-pointer truncate rounded-full border px-3 py-1.5 text-left text-[12px] transition-colors"
          class:border-brand={agent.calls.includes(other.id)}
          class:bg-brand={agent.calls.includes(other.id)}
          class:text-white={agent.calls.includes(other.id)}
          class:opacity-45={!other.enabled}
          class:border-line={!agent.calls.includes(other.id)}
          class:bg-surface={!agent.calls.includes(other.id)}
          class:text-ink-2={!agent.calls.includes(other.id)}
          title={other.description || other.name}
          disabled={!agent.enabled || !other.enabled}
          onclick={() => toggleCall(other.id)}
        >
          {other.name}
        </button>
      {/each}
    </div>
  </div>

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
      <OptionPicker
        value={agent.protocol}
        options={PROTOCOLS}
        onpick={pickProtocol}
        label="Protocol"
      />
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
    <div class="flex items-center justify-between gap-4">
      <h4 class="field-head">Thinking</h4>
      <OptionPicker
        value={agent.effort}
        options={EFFORTS}
        onpick={(effort) => onchange({ effort })}
        label="Effort"
        inline
      />
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

  <div class="field-card">
    <div class="flex items-center justify-between gap-4">
      <h4 class="field-head">Concurrency</h4>
      <input
        class="input num w-[168px] shrink-0"
        bind:value={concurrency}
        oninput={(e) => commitConcurrency(e.currentTarget.value)}
        onblur={restoreConcurrency}
        onkeydown={(e) => e.key === "Enter" && e.currentTarget.blur()}
      />
    </div>
    <p class="-mt-1 text-[12px] text-muted">
      How many tasks this agent runs at once, up to {MAX_CONCURRENCY}. Its own conversation
      counts, so an agent set to one never takes work from itself.
    </p>
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
    <div class="field-card">
      <div class="grid grid-cols-3 gap-3">
        <label class="flex flex-col gap-1.5">
          <span class="text-[13px]">Max output tokens</span>
          <input
            class="input num"
            placeholder="Auto"
            bind:value={maxTokens}
            oninput={(e) => commitMaxTokens(e.currentTarget.value)}
          />
        </label>
        <label class="flex flex-col gap-1.5">
          <span class="text-[13px]">Max context</span>
          <input
            class="input num"
            placeholder={reported > 0 ? k(reported) : "No limit"}
            bind:value={maxContext}
            oninput={(e) => commitMaxContext(e.currentTarget.value)}
          />
        </label>
        <label class="flex flex-col gap-1.5">
          <span class="text-[13px]">Seed</span>
          <input
            class="input num"
            placeholder="Random"
            bind:value={seed}
            oninput={(e) => commitSeed(e.currentTarget.value)}
          />
        </label>
      </div>
      <p class="-mt-1 text-[12px] text-muted">
        Max context is what the meter and automatic compaction measure against. Empty takes what
        the model server reports, and a server that reports nothing has no limit.
      </p>
    </div>
  </div>

  <div class="field-card !gap-2.5">
    <div class="flex items-center justify-between gap-3">
      <h4 class="field-head">What it is for</h4>
      <span class="num text-[12px] text-muted">{description.length} / {MAX_DESCRIPTION}</span>
    </div>
    <p class="-mt-1 text-[12px] text-muted">
      One line, read by the agents that may hand this one work. Empty takes the first line of the
      instructions below.
    </p>
    <input
      class="input"
      maxlength={MAX_DESCRIPTION}
      placeholder="Used when there is design work to do"
      bind:value={description}
      oninput={(e) => commitDescription(e.currentTarget.value)}
    />
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
