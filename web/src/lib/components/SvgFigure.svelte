<script lang="ts">
  import { drawing } from "../svg";

  let { source }: { source: string } = $props();

  const node = $derived(drawing(source));
  const label = $derived(node?.querySelector("title")?.textContent?.trim() || "Drawing");

  let box = $state<HTMLDivElement | null>(null);
  let shadow: ShadowRoot | null = null;

  // The drawing is mounted in a shadow root, where its own styles and ids
  // cannot reach the page or another drawing. Every token of a stream replaces
  // it, so a drawing appears as it is written.
  $effect(() => {
    if (!box || !node) return;
    shadow ??= box.attachShadow({ mode: "open" });
    shadow.replaceChildren(node);
  });
</script>

{#if node}
  <div class="md-svg" bind:this={box} role="img" aria-label={label}></div>
{:else}
  <div class="md-code">
    <div class="md-code-lang">svg</div>
    <pre><code>{source}</code></pre>
  </div>
{/if}
