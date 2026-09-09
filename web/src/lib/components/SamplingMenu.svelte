<script lang="ts">
  import CaretRight from "phosphor-svelte/lib/CaretRight";
  import Popover from "./Popover.svelte";
  import SamplingSlider from "./SamplingSlider.svelte";
  import {
    setTemperature,
    setTopP,
    temperature,
    topP,
    TEMPERATURE_STEPS,
    TOP_P_STEPS,
  } from "../state/app.svelte";

  let open = $state(false);

  const temp = $derived(temperature());
  const top = $derived(topP());
  // One word while both are the server's, and both numbers as soon as either
  // is not.
  const summary = $derived(
    temp === null && top === null
      ? "Auto"
      : `${temp === null ? "Auto" : temp.toFixed(2)} / ${top === null ? "Auto" : top.toFixed(2)}`,
  );
</script>

<div class="relative">
  <button
    class="popover-item"
    aria-haspopup="menu"
    aria-expanded={open}
    onclick={() => (open = !open)}
  >
    <span class="min-w-0 flex-1">
      <span class="block">Temperature and top p</span>
      <span class="block text-[12px] text-muted">Auto leaves both to the model server</span>
    </span>
    <span class="pill pill-mute num shrink-0 px-1.5 text-[10.5px]">{summary}</span>
    <CaretRight />
  </button>

  {#if open}
    <Popover beside onclose={() => (open = false)} width={248}>
      <SamplingSlider
        label="Temperature"
        steps={TEMPERATURE_STEPS}
        value={temp}
        onpick={setTemperature}
      />
      <SamplingSlider label="Top p" steps={TOP_P_STEPS} value={top} onpick={setTopP} />
      <p class="px-[9px] pt-1 pb-1.5 text-[12px] text-muted">
        Auto sends no field, so the model server's own value stands
      </p>
    </Popover>
  {/if}
</div>
