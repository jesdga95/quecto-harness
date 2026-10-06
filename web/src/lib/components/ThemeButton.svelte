<script lang="ts">
  import type { Component } from "svelte";
  import Monitor from "phosphor-svelte/lib/Monitor";
  import Moon from "phosphor-svelte/lib/Moon";
  import Sun from "phosphor-svelte/lib/Sun";
  import Popover from "./Popover.svelte";
  import { setTheme, theme } from "../state/theme.svelte";
  import type { ThemePref } from "../state/theme.svelte";

  let {
    align = "right",
  }: {
    /** Which way the menu opens from the button. The wide sidebar puts it to
        the left of the button; the rail has no room there. */
    align?: "left" | "right";
  } = $props();

  let open = $state(false);

  // One table drives the button and the menu. The icon on the button is the
  // one for the scheme in force, so a monitor while the operating system is
  // being followed.
  const choices = [
    {
      value: "system",
      label: "System",
      hint: "Follow the operating system.",
      icon: Monitor,
    },
    {
      value: "light",
      label: "Light",
      hint: "The light palette, whatever the operating system is set to.",
      icon: Sun,
    },
    {
      value: "dark",
      label: "Dark",
      hint: "The ink palette, whatever the operating system is set to.",
      icon: Moon,
    },
  ] as const satisfies readonly {
    value: ThemePref;
    label: string;
    hint: string;
    icon: Component;
  }[];

  const current = $derived(choices.find((c) => c.value === theme.pref) ?? choices[0]);
  const Trigger = $derived(current.icon);

  function pick(value: ThemePref) {
    open = false;
    setTheme(value);
  }
</script>

<div class="relative">
  <button
    class="icon-btn"
    aria-label={`Colour scheme: ${current.label}`}
    aria-haspopup="menu"
    aria-expanded={open}
    onclick={() => (open = !open)}
  >
    <!-- An explicit choice takes the accent, so a page following the operating
         system and a page that was told look different. -->
    <Trigger class={current.value === "system" ? "" : "text-accent"} />
  </button>

  {#if open}
    <Popover onclose={() => (open = false)} up align={align} width={250}>
      {#each choices as c (c.value)}
        {@const picked = c.value === current.value}
        <button class="popover-item" onclick={() => pick(c.value)}>
          <span class="min-w-0 flex-1">
            <span class="block" class:text-accent={picked}>{c.label}</span>
            <span class="block text-[12px] text-muted">{c.hint}</span>
          </span>
        </button>
      {/each}
    </Popover>
  {/if}
</div>
