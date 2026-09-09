<script lang="ts">
  import CaretDown from "phosphor-svelte/lib/CaretDown";
  import Check from "phosphor-svelte/lib/Check";
  import ImageIcon from "phosphor-svelte/lib/Image";
  import ArrowClockwise from "phosphor-svelte/lib/ArrowClockwise";
  import Popover from "./Popover.svelte";
  import StatusDot from "./StatusDot.svelte";
  import type { Model, Probe } from "../api";
  import { k, probeMessage } from "../format";

  let {
    value,
    models,
    probe,
    hasKey,
    checking,
    hasAddress,
    onpick,
    onrefresh,
  }: {
    value: string;
    models: Model[];
    probe: Probe | null;
    hasKey: boolean;
    checking: boolean;
    hasAddress: boolean;
    onpick: (id: string) => void;
    onrefresh: () => void;
  } = $props();

  let open = $state(false);
  // The field is typed into, so it holds a draft that the stored value refills.
  let draft = $state("");
  $effect(() => {
    draft = value;
  });

  const loaded = $derived(models.filter((m) => m.status !== "unloaded"));
  const available = $derived(models.filter((m) => m.status === "unloaded"));
  // A list is an assist, not a rule: plenty of servers answer for a model they
  // never named, so what was typed is what is sent.
  const unlisted = $derived(
    value !== "" && models.length > 0 && !models.some((m) => m.id === value),
  );
  const problem = $derived(probe ? probeMessage(probe, hasKey) : "");

  function commit() {
    if (draft.trim() !== value) onpick(draft.trim());
  }

  function pick(id: string) {
    draft = id;
    onpick(id);
    open = false;
  }
</script>

<div class="relative">
  <input
    class="input num pr-9"
    placeholder="The model this agent answers with"
    bind:value={draft}
    onblur={commit}
    onkeydown={(e) => e.key === "Enter" && e.currentTarget.blur()}
    spellcheck="false"
    autocapitalize="off"
  />
  <button
    class="icon-btn absolute top-1/2 right-1 -translate-y-1/2"
    aria-label="Choose from the models this address lists"
    aria-expanded={open}
    onclick={() => (open = !open)}
  >
    <CaretDown size={14} />
  </button>

  {#if open}
    <Popover onclose={() => (open = false)} fill>
      {#each [{ head: "Loaded", list: loaded }, { head: "Available", list: available }] as group (group.head)}
        {#if group.list.length > 0}
          <p class="popover-head">{group.head}</p>
          {#each group.list as model (model.id)}
            <button class="popover-item" onclick={() => pick(model.id)}>
              <StatusDot status={model.status} />
              <span class="min-w-0 flex-1 truncate">{model.id}</span>
              {#if model.vision}
                <ImageIcon size={14} class="text-muted" />
              {/if}
              <span class="num text-[12px] text-muted">{k(model.ctx)}</span>
              {#if model.id === value}
                <Check size={14} class="text-accent" />
              {/if}
            </button>
          {/each}
        {/if}
      {/each}
      <button class="popover-item text-ink-2" onclick={onrefresh}>
        <ArrowClockwise size={14} />
        <span class="min-w-0 flex-1">Refresh list</span>
      </button>
    </Popover>
  {/if}
</div>

{#if !hasAddress}
  <p class="mt-1.5 text-[12px] text-muted">Enter an address to list models.</p>
{:else if checking}
  <p class="mt-1.5 flex items-center gap-2 text-[12px] text-muted">
    <span class="spinner"></span> Checking the address
  </p>
{:else if problem}
  <p class="mt-1.5 text-[12px] text-crit">{problem}</p>
{:else if unlisted}
  <p class="mt-1.5 text-[12px] text-muted">That id is not in the list, and it is sent as typed.</p>
{/if}
