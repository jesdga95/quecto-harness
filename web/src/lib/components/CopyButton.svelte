<script lang="ts">
  import Check from "phosphor-svelte/lib/Check";
  import Copy from "phosphor-svelte/lib/Copy";
  import { COPIED_MS, copyText } from "../clipboard";

  // The tick stands for a second and a half, long enough to be read and short
  // enough that a second copy of the same text still shows as one.
  let { text, label, extra = "" }: { text: string; label: string; extra?: string } = $props();

  let copied = $state(false);
  let timer: ReturnType<typeof setTimeout> | undefined;

  async function copy() {
    if (!(await copyText(text))) return;
    copied = true;
    clearTimeout(timer);
    timer = setTimeout(() => (copied = false), COPIED_MS);
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
