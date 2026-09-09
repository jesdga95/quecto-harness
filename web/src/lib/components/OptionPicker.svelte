<script lang="ts" generics="T extends string">
  import Picker from "./Picker.svelte";
  import PickerRow from "./PickerRow.svelte";

  let {
    value,
    options,
    onpick,
    label,
    inline = false,
  }: {
    value: T;
    /** What can be chosen, each with the sentence saying what choosing it
        means. */
    options: { value: T; label: string; hint: string }[];
    onpick: (value: T) => void;
    label: string;
    /** Beside its heading rather than under it. */
    inline?: boolean;
  } = $props();

  let open = $state(false);
  const current = $derived(options.find((o) => o.value === value) ?? options[0]);

  function pick(next: T) {
    open = false;
    if (next !== value) onpick(next);
  }
</script>

<Picker {label} {open} {inline} onopen={() => (open = true)} onclose={() => (open = false)}>
  {#snippet trigger()}
    <button
      class="picker has-caret"
      class:is-inline={inline}
      class:is-open={open}
      aria-haspopup="listbox"
      aria-expanded={open}
      aria-label={label}
      onclick={() => (open = !open)}
    >
      <span class="min-w-0 flex-1 truncate text-left">{current?.label}</span>
    </button>
  {/snippet}

  {#each options as option (option.value)}
    <PickerRow
      label={option.label}
      hint={option.hint}
      checked={option.value === value}
      onpick={() => pick(option.value)}
    />
  {/each}
</Picker>
