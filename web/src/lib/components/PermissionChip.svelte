<script lang="ts">
  import type { Component } from "svelte";
  import CaretDown from "phosphor-svelte/lib/CaretDown";
  import Flame from "phosphor-svelte/lib/Flame";
  import Lightning from "phosphor-svelte/lib/Lightning";
  import ShieldCheck from "phosphor-svelte/lib/ShieldCheck";
  import Popover from "./Popover.svelte";
  import type { PermMode } from "../api";
  import { permMode, setPermMode } from "../state/app.svelte";

  let open = $state(false);

  // One table drives the button and the menu.
  const modes = [
    {
      value: "ask",
      label: "Ask before commands",
      hint: "Every command waits for your approval.",
      icon: ShieldCheck,
    },
    {
      value: "allowlist",
      label: "Auto with allowlist",
      hint: "Allowlisted prefixes run on their own.",
      icon: Lightning,
    },
    {
      value: "full",
      label: "Full auto",
      hint: "Anything runs, anywhere, unreviewed.",
      icon: Flame,
    },
  ] as const satisfies readonly {
    value: PermMode;
    label: string;
    hint: string;
    icon: Component;
  }[];

  const current = $derived(modes.find((m) => m.value === permMode()) ?? modes[0]);
  const Icon = $derived(current.icon);

  async function pick(value: PermMode) {
    open = false;
    await setPermMode(value);
  }
</script>

<div class="relative">
  <button class="chip chip-bare" aria-haspopup="menu" aria-expanded={open} onclick={() => (open = !open)}>
    <!-- Anything runs unreviewed, so the icon takes the colour kept for what
         needs a look. -->
    <Icon class={current.value === "full" ? "text-warn" : ""} />
    {current.label}
    <CaretDown class="chip-caret" />
  </button>

  {#if open}
    <Popover onclose={() => (open = false)} up width={280}>
      {#each modes as m (m.value)}
        <button class="popover-item" onclick={() => pick(m.value)}>
          <span class="min-w-0 flex-1">
            <span class="block" class:text-accent={m.value === current.value}>{m.label}</span>
            <span class="block text-[12px] text-muted">{m.hint}</span>
          </span>
        </button>
      {/each}
    </Popover>
  {/if}
</div>
