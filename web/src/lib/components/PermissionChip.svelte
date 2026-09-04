<script lang="ts">
  import CaretDown from "phosphor-svelte/lib/CaretDown";
  import Lightning from "phosphor-svelte/lib/Lightning";
  import ShieldCheck from "phosphor-svelte/lib/ShieldCheck";
  import Popover from "./Popover.svelte";
  import { app, setAuto } from "../state/app.svelte";

  let open = $state(false);
  const auto = $derived(app.config?.auto ?? false);

  const modes = [
    {
      auto: false,
      label: "Ask before commands",
      hint: "Every command waits for your approval.",
    },
    {
      auto: true,
      label: "Auto with allowlist",
      hint: "Allowlisted prefixes run on their own.",
    },
  ];

  function pick(value: boolean) {
    setAuto(value);
    open = false;
  }
</script>

<div class="relative">
  <button class="chip" aria-haspopup="menu" aria-expanded={open} onclick={() => (open = !open)}>
    {#if auto}
      <Lightning />
    {:else}
      <ShieldCheck />
    {/if}
    {auto ? "Auto with allowlist" : "Ask before commands"}
    <CaretDown />
  </button>

  {#if open}
    <Popover onclose={() => (open = false)} up width={280}>
      {#each modes as mode (mode.label)}
        <button class="popover-item" onclick={() => pick(mode.auto)}>
          <span class="min-w-0 flex-1">
            <span class="block" class:text-accent={auto === mode.auto}>{mode.label}</span>
            <span class="block text-[12px] text-muted">{mode.hint}</span>
          </span>
        </button>
      {/each}
    </Popover>
  {/if}
</div>
