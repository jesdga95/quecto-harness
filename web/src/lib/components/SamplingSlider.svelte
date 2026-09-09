<script lang="ts">
  import { samplingIndex } from "../state/app.svelte";

  let {
    label,
    hint,
    steps,
    value,
    onpick,
  }: {
    label: string;
    hint: string;
    steps: (number | null)[];
    value: number | null;
    onpick: (value: number | null) => void;
  } = $props();

  // The bar carries the index of a step rather than the value itself, so auto
  // is a position of its own at the left end.
  let dragged = $state<number | null>(null);
  const index = $derived(dragged ?? samplingIndex(steps, value));
  const shown = $derived(steps[index] ?? null);
  const text = $derived(shown === null ? "Auto" : format(shown));

  // A step is whole or two decimals, and the readout says which without a
  // separate setting for each slider.
  function format(v: number): string {
    return Number.isInteger(v) ? String(v) : v.toFixed(2);
  }

  function commit(i: number) {
    dragged = null;
    onpick(steps[i] ?? null);
  }
</script>

<div class="field-card !gap-2.5 !px-4 !py-3.5">
  <div class="flex items-baseline justify-between gap-3">
    <span class="text-[13px]">{label}</span>
    <span class="num text-[13px] font-medium text-accent">{text}</span>
  </div>
  <input
    class="range"
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
  <p class="text-[12px] text-muted">{hint}</p>
</div>
