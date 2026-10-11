<script lang="ts">
  import type { Component } from "svelte";
  import Monitor from "phosphor-svelte/lib/Monitor";
  import Moon from "phosphor-svelte/lib/Moon";
  import Sun from "phosphor-svelte/lib/Sun";
  import Popover from "./Popover.svelte";
  import { setFollow, setPalette, theme } from "../state/theme.svelte";
  import type { Palette } from "../state/theme.svelte";

  let {
    align = "right",
  }: {
    /** Which way the menu opens from the button. The wide sidebar puts it to
        the left of the button; the rail has no room there. */
    align?: "left" | "right";
  } = $props();

  let open = $state(false);

  interface Choice {
    value: Palette;
    label: string;
    icon: Component;
    /** The two halves of a chip: its page over its surface. A chip paints the
        palette it offers rather than the one in force, so its fills are written
        out beside its name. */
    page: string;
    surface: string;
  }

  // The order the menu shows. Only the first group is one the operating system
  // can decide between, so a reader who follows it never lands on a paper.
  const groups: { head: string; choices: Choice[] }[] = [
    {
      head: "Palette",
      choices: [
        { value: "light", label: "Light", icon: Sun, page: "#ffffff", surface: "#f1f5f9" },
        { value: "dark", label: "Dark", icon: Moon, page: "#0f172a", surface: "#1a2436" },
        {
          value: "high-contrast-light",
          label: "High contrast light",
          icon: Sun,
          page: "#ffffff",
          surface: "#e6ebf1",
        },
        {
          value: "high-contrast-dark",
          label: "High contrast dark",
          icon: Moon,
          page: "#000000",
          surface: "#0f141c",
        },
      ],
    },
    {
      head: "Paper",
      choices: [
        { value: "grey-paper", label: "Grey paper", icon: Sun, page: "#e6ebf1", surface: "#ffffff" },
        { value: "cream", label: "Cream", icon: Sun, page: "#f7f5f0", surface: "#fffefb" },
      ],
    },
  ];

  const all = groups.flatMap((g) => g.choices);
  const current = $derived(all.find((c) => c.value === theme.active) ?? all[0]);
  const Trigger = $derived(theme.follow ? Monitor : current.icon);
</script>

<div class="relative">
  <button
    class="icon-btn"
    aria-label={`Colour scheme: ${current.label}`}
    aria-haspopup="menu"
    aria-expanded={open}
    onclick={() => (open = !open)}
  >
    <!-- An explicit palette takes the accent, so a page following the operating
         system and a page that was told look different. -->
    <Trigger class={theme.follow ? "" : "text-accent"} />
  </button>

  {#if open}
    <Popover onclose={() => (open = false)} up align={align} width={250}>
      <label class="popover-item">
        <span class="min-w-0 flex-1">
          <span class="block">Follow the operating system</span>
          <span class="block text-[12px] text-muted">
            Its colour scheme and its contrast setting.
          </span>
        </span>
        <input
          class="switch"
          type="checkbox"
          checked={theme.follow}
          onchange={(e) => setFollow(e.currentTarget.checked)}
        />
      </label>

      {#each groups as g (g.head)}
        <div class="popover-head">{g.head}</div>
        {#each g.choices as c (c.value)}
          <!-- The palette in force carries the row background and, while the
               operating system is followed, the rest fade. The accent marks
               only a palette a reader named. -->
          {@const active = c.value === theme.active}
          {@const named = !theme.follow && active}
          <button
            class="popover-item"
            class:is-on={active}
            class:opacity-45={theme.follow && !active}
            onclick={() => setPalette(c.value)}
          >
            <span
              class="scheme-chip"
              style="background:linear-gradient({c.page} 50%,{c.surface} 50%)"
              aria-hidden="true"
            ></span>
            <span class="min-w-0 flex-1">
              <span class="block" class:text-accent={named}>{c.label}</span>
            </span>
          </button>
        {/each}
      {/each}
    </Popover>
  {/if}
</div>
