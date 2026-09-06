<script lang="ts">
  import CaretDown from "phosphor-svelte/lib/CaretDown";
  import Check from "phosphor-svelte/lib/Check";
  import ImageIcon from "phosphor-svelte/lib/Image";
  import Popover from "./Popover.svelte";
  import StatusDot from "./StatusDot.svelte";
  import { app, setModel } from "../state/app.svelte";
  import { k } from "../format";

  let open = $state(false);
  // There is one model, and it is the harness's. Every session uses it.
  const chosen = $derived(app.config?.model ?? "");
  const current = $derived(app.models.find((m) => m.id === chosen));
  const loaded = $derived(app.models.filter((m) => m.status !== "unloaded"));
  const available = $derived(app.models.filter((m) => m.status === "unloaded"));

  function pick(id: string) {
    void setModel(id);
    open = false;
  }
</script>

<div class="relative">
  <button class="chip" aria-haspopup="listbox" aria-expanded={open} onclick={() => (open = !open)}>
    {#if current}
      <StatusDot status={current.status} />
      <span class="max-w-[190px] truncate">{current.id}</span>
    {:else}
      <span>Select model</span>
    {/if}
    <CaretDown />
  </button>

  {#if open}
    <Popover onclose={() => (open = false)} align="right" up width={330}>
      {#each [{ head: "Loaded", list: loaded }, { head: "Available", list: available }] as group (group.head)}
        {#if group.list.length > 0}
          <p class="popover-head">{group.head}</p>
          {#each group.list as model (model.id)}
            <button class="popover-item" onclick={() => pick(model.id)}>
              <StatusDot status={model.status} />
              <span class="min-w-0 flex-1">
                <span class="block truncate">{model.id}</span>
                {#if model.status === "unloaded"}
                  <span class="block text-[12px] text-muted">Loads when you pick it</span>
                {/if}
              </span>
              {#if model.vision}
                <ImageIcon size={14} class="text-muted" />
              {/if}
              <span class="num text-[12px] text-muted">{k(model.ctx)}</span>
              {#if model.id === chosen}
                <Check size={14} class="text-accent" />
              {/if}
            </button>
          {/each}
        {/if}
      {/each}
    </Popover>
  {/if}
</div>
