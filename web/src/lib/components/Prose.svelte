<script lang="ts">
  import ArtifactCard from "./ArtifactCard.svelte";
  import { render } from "../markdown";
  import { sourceHtml } from "../highlight";

  let {
    text,
    streaming = false,
    itemId = "",
  }: {
    text: string;
    streaming?: boolean;
    /** The reply this text belongs to. Without one, a drawing, page or file
        has no panel to open in and is shown as its source. */
    itemId?: string;
  } = $props();

  // render escapes and allowlists everything it emits, which is what makes the
  // raw html below safe for output arriving from the model. A drawing, page or
  // file comes back as its source and is shown in the artifacts panel.
  const parts = $derived(render(text));
</script>

<div class="prose" class:is-streaming={streaming}>
  {#each parts as part, i (i)}
    {#if part.kind === "html"}
      {@html part.html}
    {:else if itemId}
      <ArtifactCard {itemId} block={part} />
    {:else}
      <div class="md-code">
        <div class="md-code-lang">{part.name || (part.kind === "svg" ? "svg" : "html")}</div>
        <pre><code>{@html sourceHtml(part.source, part.name)}</code></pre>
      </div>
    {/if}
  {/each}
</div>
