<script lang="ts">
  import { samplingIndex } from "../state/app.svelte";

  let {
    label,
    steps,
    value,
    onpick,
  }: {
    label: string;
    steps: (number | null)[];
    value: number | null;
    onpick: (value: number | null) => void;
  } = $props();

  // The bar carries the index of a step rather than the value itself, so auto
  // is a position of its own at the left end.
  let dragged = $state<number | null>(null);
  const index = $derived(dragged ?? samplingIndex(steps, value));
  const shown = $derived(steps[index] ?? null);
  const text = $derived(shown === null ? "Auto" : shown.toFixed(2));

  function commit(i: number) {
    dragged = null;
    onpick(steps[i] ?? null);
  }
</script>

<div class="px-[9px] pt-1.5 pb-1">
  <div class="flex items-center gap-2 text-[13px]">
    <span class="min-w-0 flex-1">{label}</span>
    <span class="pill pill-mute num w-[42px] shrink-0 justify-center px-1.5 text-[10.5px]">
      {text}
    </span>
  </div>
  <input
    class="range mt-1.5"
    type="range"
    min="0"
    max={steps.length - 1}
    step="1"
    value={index}
    aria-label={label}
    aria-valuetext={text}
    oninput={(e) => (dragged = e.currentTarget.valueAsNumber)}
    onchange={(e) => commit(e.currentTarget.valueAsNumber)}
  />
</div>
