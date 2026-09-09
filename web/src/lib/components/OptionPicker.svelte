<script lang="ts" generics="T extends string">
  import CaretDown from "phosphor-svelte/lib/CaretDown";
  import Check from "phosphor-svelte/lib/Check";
  import Popover from "./Popover.svelte";

  let {
    value,
    options,
    onpick,
    label,
  }: {
    value: T;
    /** What can be chosen, each with the sentence saying what choosing it
        means. The sentence lives on the row rather than under the field, so
        the reader sees every answer while choosing rather than one after. */
    options: { value: T; label: string; hint: string }[];
    onpick: (value: T) => void;
    label: string;
  } = $props();

  let open = $state(false);
  const current = $derived(options.find((o) => o.value === value) ?? options[0]);

  function pick(next: T) {
    open = false;
    if (next !== value) onpick(next);
  }
</script>

<div class="relative">
  <button
    class="picker"
    class:is-open={open}
    aria-haspopup="listbox"
    aria-expanded={open}
    aria-label={label}
    onclick={() => (open = !open)}
  >
    <span class="min-w-0 flex-1 truncate text-left">{current?.label}</span>
    <CaretDown size={13} class="shrink-0 text-muted" />
  </button>

  {#if open}
    <Popover onclose={() => (open = false)} fill float>
      {#each options as option (option.value)}
        <button class="popover-item items-start" onclick={() => pick(option.value)}>
          <span class="min-w-0 flex-1">
            <span class="block text-left">{option.label}</span>
            <span class="block text-left text-[12px] leading-snug text-muted">{option.hint}</span>
          </span>
          {#if option.value === value}
            <Check size={14} class="mt-0.5 text-accent" />
          {/if}
        </button>
      {/each}
    </Popover>
  {/if}
</div>
