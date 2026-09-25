// The drawings, pages and files a session's replies hold, in the order they
// were written. They are read from the transcript itself, so they last as long
// as the session does and nothing about them is stored.

import type { TranscriptItem } from "./api";
import { blocksOf, type Block } from "./markdown";
import { titleOf } from "./markup";

export interface Artifact {
  /** The reply's item id and the block's place in that reply. */
  id: string;
  itemId: string;
  kind: "svg" | "page" | "file";
  /** The file name the fence gave, or empty. */
  name: string;
  source: string;
  complete: boolean;
  /** The file name, else the markup's own title, else the kind, with a
      version when it repeats. */
  title: string;
}

/** What an artifact is called before any version is added. */
export function baseTitle(block: Block): string {
  if (block.kind === "file") return block.name;
  return titleOf(block.source) || block.name || (block.kind === "svg" ? "Drawing" : "Page");
}

export function artifactId(itemId: string, index: number): string {
  return `${itemId}:${index}`;
}

// A reply is scanned again only when its text has changed, which during a
// stream is the reply being written and nothing else.
const cache = new Map<string, { text: string; blocks: Block[] }>();

function blocksOfItem(id: string, text: string): Block[] {
  const hit = cache.get(id);
  if (hit && hit.text === text) return hit.blocks;
  const blocks = blocksOf(text);
  cache.set(id, { text, blocks });
  if (cache.size > 2000) cache.clear();
  return blocks;
}

export function artifactsOf(items: TranscriptItem[]): Artifact[] {
  const out: Artifact[] = [];
  const seen = new Map<string, number>();
  for (const item of items) {
    if (item.kind !== "assistant") continue;
    for (const block of blocksOfItem(item.id, item.text)) {
      const base = baseTitle(block);
      const n = (seen.get(base) ?? 0) + 1;
      seen.set(base, n);
      out.push({
        id: artifactId(item.id, block.index),
        itemId: item.id,
        kind: block.kind,
        name: block.name,
        source: block.source,
        complete: block.complete,
        title: n > 1 ? `${base} v${n}` : base,
      });
    }
  }
  return out;
}
