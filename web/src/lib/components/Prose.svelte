<script lang="ts">
  import { render } from "../markdown";
  import { openFile } from "../state/app.svelte";
  import { Streamed } from "../stream.svelte";

  let { text, streaming = false }: { text: string; streaming?: boolean } = $props();

  const stream = new Streamed(
    () => text,
    () => streaming,
  );

  // render escapes and allowlists everything it emits, which is what makes the
  // raw html below safe for output arriving from the model.
  const html = $derived(render(stream.text));

  let box = $state<HTMLDivElement | null>(null);
  $effect(() => {
    if (box) stream.draw(box, html);
  });

  // A file link is markup rather than a component, so one listener on the
  // block answers for every link in it, and each stays a real button.
  function fileLinks(node: HTMLElement) {
    const click = (event: MouseEvent) => {
      const link = (event.target as HTMLElement).closest<HTMLElement>("[data-file]");
      if (!link || !node.contains(link)) return;
      event.preventDefault();
      void openFile(link.dataset.file ?? "");
    };
    node.addEventListener("click", click);
    return { destroy: () => node.removeEventListener("click", click) };
  }
</script>

<div class="prose" class:is-streaming={stream.busy} bind:this={box} use:fileLinks></div>
