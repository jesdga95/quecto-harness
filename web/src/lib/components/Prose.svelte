<script lang="ts">
  import { render } from "../markdown";

  let { text, streaming = false }: { text: string; streaming?: boolean } = $props();

  // render escapes and allowlists everything it emits, which is what makes
  // the raw html below safe for output arriving from the model.
  const html = $derived(render(text));
</script>

<div class="prose" class:is-streaming={streaming}>{@html html}</div>
