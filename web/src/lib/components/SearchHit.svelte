<script lang="ts">
  import { untrack } from "svelte";
  import type { SearchHit } from "../search";

  let { hit, index }: { hit: SearchHit; index: number } = $props();

  let open = $state(false);
  let over = $state(false);
  let box: HTMLDivElement | undefined = $state();

  // What the card shows under the title: the site it came from, or that the
  // address was refused, and the day the page carries when it carries one.
  const sub = $derived(
    [hit.url === null ? "No address" : hit.host, hit.published].filter((s) => s !== "").join(" · "),
  );
  const title = $derived(hit.title !== "" ? hit.title : hit.host);

  // Whether the passages run past the three lines the card holds is measured
  // at the width the card ended up, so the control appears only where there
  // is something behind it. Measured while the card is closed, since an open
  // one is exactly as tall as its contents.
  $effect(() => {
    const el = box;
    if (!el) return;
    const measure = () => {
      if (untrack(() => open)) return;
      over = el.scrollHeight - el.clientHeight > 1;
    };
    measure();
    const watch = new ResizeObserver(measure);
    watch.observe(el);
    return () => watch.disconnect();
  });
</script>

<article class="hit">
  <div class="hit-head">
    <span class="hit-n">{index + 1}</span>
    <span class="hit-glyph">{(hit.host || hit.title || "?").charAt(0)}</span>
    <span class="hit-titles">
      {#if hit.url !== null}
        <a class="hit-title" href={hit.url} target="_blank" rel="noopener noreferrer">{title}</a>
      {:else}
        <span class="hit-title is-plain">{title}</span>
      {/if}
      {#if sub !== ""}
        <span class="hit-sub">{sub}</span>
      {/if}
    </span>
  </div>

  {#if hit.body.length > 0}
    <div class="hit-body" class:is-clamped={!open} bind:this={box}>
      {#each hit.body as passage, i (i)}
        <p class="hit-frag">{passage}</p>
      {/each}
    </div>
    {#if over}
      <button class="hit-more" onclick={() => (open = !open)}>
        {open ? "Show less" : "Read more"}
      </button>
    {/if}
  {/if}
</article>
