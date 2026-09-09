<script lang="ts">
  import CaretDown from "phosphor-svelte/lib/CaretDown";
  import Check from "phosphor-svelte/lib/Check";
  import ImageIcon from "phosphor-svelte/lib/Image";
  import ArrowClockwise from "phosphor-svelte/lib/ArrowClockwise";
  import Popover from "./Popover.svelte";
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
  // Whether the draft is being read as a search or is only the model the field
  // was opened on. Without this an address that lists thousands would answer
  // the caret with the one row already chosen.
  let searching = $state(false);
  $effect(() => {
    draft = value;
  });

  // How many rows are drawn. An address can list thousands, each one a node,
  // and the top of such a list reads no better than the request to narrow it.
  const SHOWN = 60;
  const query = $derived(searching ? draft.trim().toLowerCase() : "");
  const hits = $derived(
    query === "" ? models : models.filter((m) => m.id.toLowerCase().includes(query)),
  );
  const rows = $derived(hits.slice(0, SHOWN));
  // A list is an assist, not a rule: plenty of servers answer for a model they
  // never named, so what was typed is what is sent.
  const unlisted = $derived(
    value !== "" && models.length > 0 && !models.some((m) => m.id === value),
  );
  const problem = $derived(probe ? probeMessage(probe, hasKey) : "");

  function type(text: string) {
    searching = true;
    open = true;
    if (text.trim() !== value) onpick(text.trim());
  }

  function pick(id: string) {
    draft = id;
    onpick(id);
    close();
  }

  function close() {
    searching = false;
    open = false;
  }
</script>

<div class="relative">
  <input
    class="input num pr-9"
    placeholder="The model this agent answers with"
    bind:value={draft}
    oninput={(e) => type(e.currentTarget.value)}
    onkeydown={(e) => e.key === "Enter" && e.currentTarget.blur()}
    spellcheck="false"
    autocapitalize="off"
  />
  <button
    class="icon-btn absolute top-1/2 right-1 -translate-y-1/2"
    aria-label="Choose from the models this address lists"
    aria-expanded={open}
    onclick={() => (open ? close() : (open = true))}
  >
    <CaretDown size={14} />
  </button>

  {#if open}
    <Popover onclose={close} fill float>
      <div class="max-h-[248px] overflow-y-auto">
        {#each rows as model (model.id)}
          <button class="popover-item" onclick={() => pick(model.id)}>
            <span class="min-w-0 flex-1 truncate text-left">{model.id}</span>
            {#if model.vision}
              <ImageIcon size={14} class="text-muted" />
            {/if}
            <span class="num text-[12px] text-muted">{k(model.ctx)}</span>
            {#if model.id === value}
              <Check size={14} class="text-accent" />
            {/if}
          </button>
        {:else}
          <p class="px-[9px] py-1.5 text-[12px] text-muted">
            {models.length === 0
              ? "This address listed no models."
              : "Nothing in the list matches what you typed."}
          </p>
        {/each}
      </div>
      {#if hits.length > rows.length}
        <p class="px-[9px] py-1.5 text-[12px] text-muted">
          {hits.length - rows.length} more. Keep typing to narrow the list.
        </p>
      {/if}
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
