<script lang="ts">
  import Check from "phosphor-svelte/lib/Check";
  import Copy from "phosphor-svelte/lib/Copy";
  import Quotes from "phosphor-svelte/lib/Quotes";
  import { addQuote } from "../state/quotes.svelte";

  // Floats over text selected inside a reply. Only a selection that starts and
  // ends inside one element marked data-quote opens it.

  let el = $state<HTMLDivElement | null>(null);
  let open = $state(false);
  let picked = "";
  let copied = $state(false);
  let box = $state({ top: 0, left: 0 });

  const GAP = 8;
  const EDGE = 8;

  function selected(): Range | null {
    const sel = getSelection();
    if (!sel || sel.isCollapsed || sel.rangeCount === 0) return null;
    const range = sel.getRangeAt(0);
    const start = holder(range.startContainer);
    if (!start || start !== holder(range.endContainer)) return null;
    return sel.toString().trim() ? range : null;
  }

  function holder(node: Node): Element | null {
    const at = node instanceof Element ? node : node.parentElement;
    return at?.closest("[data-quote]") ?? null;
  }

  // Centred over the first line of the selection, or under the last line when
  // the first is too close to the top of the window.
  function place() {
    const range = selected();
    if (!range || !el) {
      open = false;
      return;
    }
    const rects = [...range.getClientRects()].filter((r) => r.width > 0);
    const first = rects[0];
    const last = rects.at(-1);
    if (!first || !last) {
      open = false;
      return;
    }
    const w = el.offsetWidth;
    const h = el.offsetHeight;
    const above = first.top - h - GAP >= EDGE;
    const anchor = above ? first : last;
    const left = anchor.left + anchor.width / 2 - w / 2;
    box = {
      top: above ? first.top - h - GAP : last.bottom + GAP,
      left: Math.max(EDGE, Math.min(left, window.innerWidth - w - EDGE)),
    };
  }

  function show() {
    if (!selected()) {
      open = false;
      return;
    }
    picked = getSelection()?.toString().trim() ?? "";
    copied = false;
    open = true;
    requestAnimationFrame(place);
  }

  function quote() {
    addQuote(picked);
    getSelection()?.removeAllRanges();
    open = false;
  }

  function copy() {
    void navigator.clipboard?.writeText(picked);
    copied = true;
  }

  function onmouseup(event: MouseEvent) {
    if (el?.contains(event.target as Node)) return;
    setTimeout(show, 0);
  }

  function onkeyup(event: KeyboardEvent) {
    if (event.shiftKey && event.key.startsWith("Arrow")) show();
  }

  // Q quotes and Escape lets the selection go, while the popover is open and
  // the key is not meant for a field.
  function onkeydown(event: KeyboardEvent) {
    if (!open || event.defaultPrevented || event.metaKey || event.ctrlKey || event.altKey) return;
    const target = event.target;
    if (target instanceof Element && target.closest("input, textarea, select, [contenteditable]")) return;
    if (event.key === "q" || event.key === "Q") {
      event.preventDefault();
      quote();
    } else if (event.key === "Escape") {
      event.preventDefault();
      getSelection()?.removeAllRanges();
      open = false;
    }
  }

  $effect(() => {
    const changed = () => {
      if (open && !selected()) open = false;
    };
    const moved = () => {
      if (open) place();
    };
    document.addEventListener("selectionchange", changed);
    window.addEventListener("scroll", moved, true);
    window.addEventListener("resize", moved);
    return () => {
      document.removeEventListener("selectionchange", changed);
      window.removeEventListener("scroll", moved, true);
      window.removeEventListener("resize", moved);
    };
  });
</script>

<svelte:window {onmouseup} {onkeyup} {onkeydown} />

<div
  bind:this={el}
  class="popover quote-pop"
  class:is-open={open}
  style="top: {box.top}px; left: {box.left}px"
  role="toolbar"
  aria-label="Selection"
  aria-hidden={!open}
  onmousedown={(e) => e.preventDefault()}
>
  <button class="quote-pop-btn" tabindex={open ? 0 : -1} onclick={quote}>
    <Quotes size={15} weight="fill" class="text-accent" />
    Quote
    <span class="quote-pop-key">Q</span>
  </button>
  <span class="quote-pop-sep"></span>
  <button class="quote-pop-btn" tabindex={open ? 0 : -1} onclick={copy}>
    {#if copied}
      <Check size={15} class="text-muted" />
      Copied
    {:else}
      <Copy size={15} class="text-muted" />
      Copy
    {/if}
  </button>
</div>
