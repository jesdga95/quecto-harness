<script lang="ts">
  import { escapeHtml } from "../highlight";
  import { Streamed } from "../stream.svelte";

  // Plain text the model is still writing, paced like a streamed reply.
  let { text, live = false, class: klass = "" }: { text: string; live?: boolean; class?: string } =
    $props();

  const stream = new Streamed(
    () => text,
    () => live,
  );

  let box = $state<HTMLParagraphElement | null>(null);
  $effect(() => {
    if (box) stream.draw(box, escapeHtml(stream.text));
  });
</script>

<p class={klass} bind:this={box}></p>
