<script lang="ts">
  import HtmlFrame from "./HtmlFrame.svelte";
  import SvgFigure from "./SvgFigure.svelte";
  import { render } from "../markdown";

  let { text, streaming = false }: { text: string; streaming?: boolean } = $props();

  // render escapes and allowlists everything it emits, which is what makes the
  // raw html below safe for output arriving from the model. A drawing comes
  // back as its source and is sanitized where it is mounted, and a page runs
  // in a frame of its own.
  const parts = $derived(render(text));
</script>

<div class="prose" class:is-streaming={streaming}>
  {#each parts as part, i (i)}
    {#if part.kind === "html"}
      {@html part.html}
    {:else if part.kind === "svg"}
      <SvgFigure source={part.source} />
    {:else}
      <HtmlFrame source={part.source} />
    {/if}
  {/each}
</div>
