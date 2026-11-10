<script lang="ts">
  import { COPIED_MS, copyText } from "../clipboard";
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

  // A redraw replaces every copy button, so none is drawn while one is pressed,
  // and the tick is kept by the block's position and put back after each draw.
  let pressing = $state(false);
  let copied = $state<number | null>(null);

  let box = $state<HTMLDivElement | null>(null);
  $effect(() => {
    if (!box || pressing) return;
    stream.draw(box, html);
    if (copied !== null) box.querySelectorAll("[data-copy]")[copied]?.classList.add("is-copied");
  });

  // A file link is markup rather than a component, so one listener on the
  // block answers for every link in it, and each stays a real button.
  function fileLinks(node: HTMLElement) {
    const click = (event: MouseEvent) => {
      const link = (event.target as HTMLElement).closest<HTMLElement>("[data-file]");
      if (!link || !node.contains(link)) return;
      event.preventDefault();
      const line = Number(link.dataset.line ?? 0);
      void openFile(link.dataset.file ?? "", Number.isFinite(line) ? line : 0);
    };
    node.addEventListener("click", click);
    return { destroy: () => node.removeEventListener("click", click) };
  }

  // The nth code block of the whole source, which the reveal may not have
  // drawn in full yet.
  function blockCode(index: number): string | null {
    const full = document.createElement("div");
    full.innerHTML = render(text);
    return full.querySelectorAll(".md-code pre code")[index]?.textContent ?? null;
  }

  function codeCopies(node: HTMLElement) {
    let timer: ReturnType<typeof setTimeout> | undefined;
    const press = (event: PointerEvent) => {
      if ((event.target as HTMLElement).closest("[data-copy]")) pressing = true;
    };
    // Released after the click that follows, which needs the pressed button.
    const release = () => setTimeout(() => (pressing = false));
    const click = async (event: MouseEvent) => {
      const button = (event.target as HTMLElement).closest<HTMLElement>("[data-copy]");
      if (!button || !node.contains(button)) return;
      const index = [...node.querySelectorAll("[data-copy]")].indexOf(button);
      const code = blockCode(index);
      if (code === null || !(await copyText(code))) return;
      for (const other of node.querySelectorAll("[data-copy].is-copied")) other.classList.remove("is-copied");
      button.classList.add("is-copied");
      copied = index;
      clearTimeout(timer);
      timer = setTimeout(() => {
        copied = null;
        node.querySelectorAll("[data-copy].is-copied").forEach((b) => b.classList.remove("is-copied"));
      }, COPIED_MS);
    };
    node.addEventListener("pointerdown", press);
    window.addEventListener("pointerup", release);
    window.addEventListener("pointercancel", release);
    node.addEventListener("click", click);
    return {
      destroy: () => {
        node.removeEventListener("pointerdown", press);
        window.removeEventListener("pointerup", release);
        window.removeEventListener("pointercancel", release);
        node.removeEventListener("click", click);
        clearTimeout(timer);
      },
    };
  }
</script>

<div class="prose" class:is-streaming={stream.busy} bind:this={box} use:fileLinks use:codeCopies></div>
