<script lang="ts">
  import { render } from "../markdown";
  import { openFile } from "../state/app.svelte";

  let { text, streaming = false }: { text: string; streaming?: boolean } = $props();

  // render escapes and allowlists everything it emits, which is what makes the
  // raw html below safe for output arriving from the model.
  const html = $derived(render(text));

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

<div class="prose" class:is-streaming={streaming} use:fileLinks>
  {@html html}
</div>
