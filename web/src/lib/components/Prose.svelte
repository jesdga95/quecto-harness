<script lang="ts">
  import SvgFigure from "./SvgFigure.svelte";
  import { render } from "../markdown";

  let { text, streaming = false }: { text: string; streaming?: boolean } = $props();

  // render escapes and allowlists everything it emits, which is what makes the
  // raw html below safe for output arriving from the model. A drawing comes
  // back as its source and is sanitized where it is mounted.
  const parts = $derived(render(text));
</script>

<div class="prose" class:is-streaming={streaming}>
  {#each parts as part, i (i)}
    {#if part.kind === "svg"}
      <SvgFigure source={part.source} />
    {:else}
      {@html part.html}
    {/if}
  {/each}
</div>
