<script lang="ts">
  import Check from "phosphor-svelte/lib/Check";
  import Copy from "phosphor-svelte/lib/Copy";

  // The tick stands for a second and a half, long enough to be read and short
  // enough that a second copy of the same text still shows as one.
  let { text, label, extra = "" }: { text: string; label: string; extra?: string } = $props();

  let copied = $state(false);
  let timer: ReturnType<typeof setTimeout> | undefined;

  function copy() {
    void navigator.clipboard?.writeText(text);
    copied = true;
    clearTimeout(timer);
    timer = setTimeout(() => (copied = false), 1500);
  }

  $effect(() => () => clearTimeout(timer));
</script>

<button class="icon-btn {extra}" class:is-copied={copied} aria-label={label} onclick={copy}>
  {#if copied}
    <Check />
  {:else}
    <Copy />
  {/if}
</button>
