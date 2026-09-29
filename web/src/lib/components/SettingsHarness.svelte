<script lang="ts">
  import type { Config, Toolset, ToolSpec } from "../api";
  import { app, loadToolDebug } from "../state/app.svelte";
  import OptionPicker from "./OptionPicker.svelte";

  let {
    config,
    onchange,
  }: {
    config: Config;
    onchange: (patch: Partial<Config>) => void;
  } = $props();

  const MODES: { value: Toolset; label: string; hint: string }[] = [
    { value: "full", label: "Full", hint: "Every tool the harness ships with. Nothing is held back." },
    {
      value: "minimal",
      label: "Minimal",
      hint: "Reads and edits files, runs commands. No web, no delegation, no plans.",
    },
  ];

  // A read of the harness, fetched on each visit. The last answer stays drawn
  // while the new one is in flight.
  void loadToolDebug();

  const data = $derived(app.toolDebug);
  const sentNames = $derived(data?.sent[config.toolset] ?? []);
  const sentSpecs = $derived(
    sentNames
      .map((n) => data?.tools.find((t) => t.name === n))
      .filter((t): t is ToolSpec => t !== undefined),
  );
  const total = $derived(data?.tools.length ?? 0);

  export function flush() {}
</script>

<div class="field-card">
  <div class="flex flex-col gap-1.5">
    <h4 class="field-head">Harness mode</h4>
    <span class="text-[11.5px] text-muted">
      Which tools the model can reach for. Applies to new sessions: a session keeps the tools it was
      started with.
    </span>
    <OptionPicker
      value={config.toolset}
      options={MODES}
      onpick={(m: Toolset) => onchange({ toolset: m })}
      label="Harness mode"
    />
  </div>
</div>

{#if data === null}
  <div class="field-card">
    <span class="label">Loading the tool list…</span>
  </div>
{:else}
  <div class="field-card debug-card">
    <div class="flex items-center justify-between gap-3">
      <h4 class="field-head">What a turn sends</h4>
      <span class="text-[11.5px] text-muted">{sentNames.length} of {total} tools</span>
    </div>
    <span class="text-[11.5px] text-muted">In the order the model sees them. Open a card for its parameters.</span>

    {#each sentSpecs as spec (spec.name)}
      <details class="tool-card">
        <summary>
          <svg class="chev" viewBox="0 0 16 16" width="13" height="13" aria-hidden="true">
            <path
              d="M6 4l4 4-4 4"
              fill="none"
              stroke="currentColor"
              stroke-width="1.6"
              stroke-linecap="round"
              stroke-linejoin="round"
            />
          </svg>
          <span class="tool-name mono">{spec.name}</span>
          <span class="tool-params">
            {spec.params.length} {spec.params.length === 1 ? "parameter" : "parameters"}
          </span>
        </summary>
        <div class="tool-body">
          <p class="tool-desc">{spec.description}</p>
          {#if spec.params.length > 0}
            <table class="param-table">
              <thead>
                <tr>
                  <th>Parameter</th>
                  <th>Type</th>
                  <th>Description</th>
                </tr>
              </thead>
              <tbody>
                {#each spec.params as p (p.name)}
                  <tr>
                    <td class="param-name mono">{p.name}{#if p.required}<span class="req"> *</span>{/if}</td>
                    <td class="param-type mono">
                      {p.type}
                      {#if p.minimum !== undefined}<span class="min">· min {p.minimum}</span>{/if}
                    </td>
                    <td class="param-desc">{p.description}</td>
                  </tr>
                {/each}
              </tbody>
            </table>
          {/if}
        </div>
      </details>
    {/each}
  </div>
{/if}

<style>
  .mono {
    font-family: var(--font-mono);
  }
  .debug-card {
    align-items: flex-start;
  }
  .debug-card > .flex {
    width: 100%;
  }

  .tool-card {
    width: 100%;
    border: 1px solid var(--hair);
    border-radius: 10px;
    overflow: hidden;
  }
  .tool-card summary {
    display: flex;
    align-items: center;
    gap: 9px;
    padding: 9px 12px;
    cursor: pointer;
    list-style: none;
    user-select: none;
  }
  .tool-card summary::-webkit-details-marker {
    display: none;
  }
  .tool-card summary:hover {
    background: var(--surface-2);
  }
  .chev {
    color: var(--text-2);
    transition: transform 90ms ease;
    flex: none;
  }
  .tool-card[open] .chev {
    transform: rotate(90deg);
  }
  .tool-name {
    font-size: 13px;
    font-weight: 500;
  }
  .tool-params {
    margin-left: auto;
    font-size: 11.5px;
    color: var(--text-2);
    white-space: nowrap;
  }
  .tool-body {
    display: flex;
    flex-direction: column;
    gap: 9px;
    padding: 11px 12px 12px;
    border-top: 1px solid var(--hair);
    background: var(--surface-2);
  }
  .tool-desc {
    margin: 0;
    font-size: 12px;
    line-height: 1.5;
    color: var(--text-2);
  }
  .param-table {
    width: 100%;
    border-collapse: collapse;
    font-size: 12px;
  }
  .param-table th {
    text-align: left;
    font-weight: 500;
    font-size: 11px;
    letter-spacing: 0.02em;
    text-transform: uppercase;
    color: var(--text-2);
    padding: 0 8px 5px 0;
  }
  .param-table td {
    padding: 5px 8px 5px 0;
    vertical-align: top;
    border-top: 1px solid var(--hair);
  }
  .param-name {
    white-space: nowrap;
    font-weight: 500;
  }
  .req {
    color: var(--text-2);
  }
  .param-type {
    white-space: nowrap;
    color: var(--text-2);
  }
  .min {
    font-size: 11px;
  }
  .param-desc {
    color: var(--text-2);
    line-height: 1.45;
  }
</style>
