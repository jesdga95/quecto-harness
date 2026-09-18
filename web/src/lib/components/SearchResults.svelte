<script lang="ts">
  import type { TranscriptItem } from "../api";
  import { safeUrl } from "../markdown";

  let { item }: { item: Extract<TranscriptItem, { kind: "tool" }> } = $props();

  interface Hit {
    title: string;
    url: string | null;
    published: string;
  }

  // The meta carries where the pages are; the bodies are in the result below,
  // so the list links rather than repeating them. The endpoint address is
  // user-configurable, so a hit's url goes through the same gate as a link
  // the model wrote, and a hit whose url does not pass it is named rather
  // than linked.
  const hits = $derived.by<Hit[]>(() => {
    const raw = item.meta?.hits;
    if (!Array.isArray(raw)) return [];
    return raw
      .filter((h): h is Record<string, unknown> => typeof h === "object" && h !== null)
      .map((h) => ({
        title: typeof h.title === "string" ? h.title : "",
        url: safeUrl(typeof h.url === "string" ? h.url : ""),
        published: typeof h.published === "string" ? h.published : "",
      }))
      .filter((h) => h.url !== null || h.title !== "");
  });
  const dropped = $derived(typeof item.meta?.dropped === "number" ? (item.meta?.dropped as number) : 0);

  function host(url: string | null): string {
    if (!url) return "";
    try {
      return new URL(url).host;
    } catch {
      return url;
    }
  }

  // The endpoint dates are full timestamps; a row wants the day.
  function day(iso: string): string {
    return iso.length > 10 ? iso.slice(0, 10) : iso;
  }
</script>

{#if hits.length > 0}
  <ul class="flex flex-col gap-2">
    {#each hits as hit, i (i)}
      <li class="flex flex-col gap-0.5">
        {#if hit.url !== null}
          <a
            class="text-[13px] font-medium text-accent hover:underline"
            href={hit.url}
            target="_blank"
            rel="noopener noreferrer"
          >
            {hit.title || host(hit.url)}
          </a>
        {:else}
          <span class="text-[13px] font-medium text-accent">{hit.title}</span>
        {/if}
        <span class="text-[12px] text-muted">
          {#if hit.url === null && hit.published !== ""}
            {day(hit.published)}
          {:else}
            {host(hit.url)}{hit.published ? ` · ${day(hit.published)}` : ""}
          {/if}
        </span>
      </li>
    {/each}
  </ul>
{/if}

{#if item.output}
  <pre class="mono-block">{item.output}</pre>
{/if}

{#if dropped > 0}
  <span class="text-[11.5px] text-muted">
    {dropped} {dropped === 1 ? "result" : "results"} came back with no address and were dropped.
  </span>
{/if}

{#if item.truncated}
  <span class="text-[11.5px] text-muted">Truncated, head and tail kept</span>
{/if}
