<script lang="ts">
  import { drawing } from "../svg";

  let { source, complete }: { source: string; complete: boolean } = $props();

  const node = $derived(drawing(source));

  let box = $state<HTMLDivElement | null>(null);
  let shadow: ShadowRoot | null = null;

  // The drawing is mounted in a shadow root, where its own styles and ids
  // cannot reach the page. Every token of a stream replaces it, so a drawing
  // appears as it is written.
  $effect(() => {
    if (!box || !node) return;
    shadow ??= box.attachShadow({ mode: "open" });
    shadow.replaceChildren(node);
  });
</script>

<div class="artifact-paper">
  {#if node}
    <div class="artifact-svg" bind:this={box} role="img" aria-label="Drawing"></div>
  {:else}
    <p class="artifact-wait">
      {complete ? "This drawing has no size or viewBox, so there is nothing to scale." : "Writing the drawing"}
    </p>
  {/if}
</div>
