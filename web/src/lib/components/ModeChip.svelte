<script lang="ts">
  import type { Component } from "svelte";
  import CaretDown from "phosphor-svelte/lib/CaretDown";
  import ListChecks from "phosphor-svelte/lib/ListChecks";
  import Play from "phosphor-svelte/lib/Play";
  import Popover from "./Popover.svelte";
  import type { WorkMode } from "../api";
  import { app, setWorkMode } from "../state/app.svelte";

  let open = $state(false);

  // One table drives the button and the menu.
  const modes = [
    {
      value: "plan",
      label: "Plan",
      hint: "Reads, designs, then sends a plan to approve. Files are read only.",
      icon: ListChecks,
    },
    {
      value: "execute",
      label: "Execute",
      hint: "Changes files and runs commands.",
      icon: Play,
    },
  ] as const satisfies readonly {
    value: WorkMode;
    label: string;
    hint: string;
    icon: Component;
  }[];

  const current = $derived(modes.find((m) => m.value === app.workMode) ?? modes[1]);
  const Icon = $derived(current.icon);

  async function pick(value: WorkMode) {
    open = false;
    await setWorkMode(value);
  }
</script>

<div class="relative">
  <button
    class="chip chip-bare"
    class:chip-mode={current.value !== "execute"}
    title="Switch with Shift+Tab"
    aria-label={current.label}
    aria-haspopup="menu"
    aria-expanded={open}
    onclick={() => (open = !open)}
  >
    <Icon />
    <span class="chip-mode-label">{current.label}</span>
    <CaretDown class="chip-caret" />
  </button>

  {#if open}
    <Popover onclose={() => (open = false)} up width={300}>
      {#each modes as m (m.value)}
        <button class="popover-item" onclick={() => pick(m.value)}>
          <span class="min-w-0 flex-1">
            <span
              class="block"
              class:text-accent={m.value === current.value && m.value === "execute"}
              class:text-plan={m.value === current.value && m.value === "plan"}>{m.label}</span
            >
            <span class="block text-[12px] text-muted">{m.hint}</span>
          </span>
        </button>
      {/each}
    </Popover>
  {/if}
</div>
