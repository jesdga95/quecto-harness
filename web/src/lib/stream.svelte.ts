// Smooth streaming for text the model writes. Text arrives in bursts, so it is
// revealed at a steady pace instead, a whole word at a time and a moment behind
// the source, and each word fades in as it appears.

import { untrack } from "svelte";

// How far behind the source the reveal may fall before it speeds up to catch
// up, and the slowest it goes. Rates are characters a second.
const CATCH_UP = 0.3;
const MIN_RATE = 90;
// Matches the .fresh animation in prose.css.
const FADE = 420;

const still = () =>
  typeof matchMedia === "function" && matchMedia("(prefers-reduced-motion: reduce)").matches;

/** Reveals a growing source at a steady pace. Create it while a component
    initialises, since it runs effects of its own. */
export class Streamed {
  #source: () => string;
  #live: () => boolean;
  #key: () => unknown;
  #lastKey: unknown;
  /** How much of the source is on screen. */
  shown = $state(0);
  #settling = $state(false);
  // A source that was already whole when it was first seen is shown at once.
  #wasLive = false;
  // When the drawn text reached each length, for the words still fading.
  #stamps: { len: number; at: number }[] = [];
  #settled = 0;
  // What a view opened on is old text, drawn plain: only what arrives after fades.
  #opened = true;

  #busy: boolean;

  /** key names what the source is, for a view that shows one thing and then
      another: a new key starts from what the new source already holds. */
  constructor(source: () => string, live: () => boolean, key: () => unknown = () => null) {
    this.#source = source;
    this.#live = live;
    this.#key = key;
    this.#lastKey = key();
    this.#busy = $derived(this.#live() || this.shown < this.#source().length);
    // What is already there when a view opens is shown at once, and what
    // arrives after is paced.
    this.shown = source().length;

    $effect(() => {
      const len = this.#source().length;
      const live = this.#live();
      const key = this.#key();
      if (key !== this.#lastKey) {
        this.#lastKey = key;
        this.#wasLive = false;
        this.#opened = true;
        this.shown = len;
      }
      if (live) this.#wasLive = true;
      const at = untrack(() => this.shown);
      if (still() || (!live && !this.#wasLive) || at > len) {
        this.shown = len;
        return;
      }
      if (at >= len) {
        if (!live) this.#wasLive = false;
        return;
      }
      let last = performance.now();
      let frame = 0;
      const step = (now: number) => {
        const dt = Math.min(now - last, 64) / 1000;
        last = now;
        const moved = this.#advance(dt);
        const caught = this.shown >= this.#source().length;
        if (caught && !this.#live()) this.#wasLive = false;
        // A held word waits for the next change to the source, which runs this again.
        frame = caught || !moved ? 0 : requestAnimationFrame(step);
      };
      frame = requestAnimationFrame(step);
      return () => cancelAnimationFrame(frame);
    });

    // The last words finish fading before the text is drawn plain again.
    $effect(() => {
      if (this.#busy) {
        this.#settling = true;
        return;
      }
      const done = setTimeout(() => (this.#settling = false), FADE);
      return () => clearTimeout(done);
    });
  }

  // Moves the reveal on by dt seconds. Returns false when it cannot move.
  #advance(dt: number): boolean {
    const src = this.#source();
    const from = this.shown;
    const rate = Math.max(MIN_RATE, (src.length - from) / CATCH_UP);
    let next = Math.min(src.length, from + Math.max(1, Math.round(rate * dt)));
    // Whole words only, and a word still arriving waits for the rest of it.
    while (next < src.length && !/\s/.test(src[next] ?? "")) next += 1;
    if (next === src.length && this.#live() && !/\s$/.test(src)) {
      const gap = Math.max(src.lastIndexOf(" ", next - 1), src.lastIndexOf("\n", next - 1));
      if (gap + 1 <= from) return false;
      next = gap + 1;
    }
    this.shown = next;
    return true;
  }

  /** The part of the source on screen. */
  get text(): string {
    return this.#source().slice(0, this.shown);
  }

  /** True while the source streams or the reveal is behind it. */
  get busy(): boolean {
    return this.#busy;
  }

  /** Draws html into node, with the words that appeared in the last moment
      wrapped to fade in. Outside a stream it is drawn plain. */
  draw(node: HTMLElement, html: string): void {
    node.innerHTML = html;
    if (still() || !(this.#busy || this.#settling)) {
      this.#opened = true;
      return;
    }
    const now = performance.now();
    const len = node.textContent?.length ?? 0;
    if (this.#opened) {
      this.#opened = false;
      this.#stamps = [];
      this.#settled = len;
      return;
    }
    const top = this.#stamps.at(-1)?.len ?? this.#settled;
    if (len > top) this.#stamps.push({ len, at: now });
    while (this.#stamps.length > 0 && now - (this.#stamps[0]?.at ?? now) >= FADE) {
      this.#settled = this.#stamps.shift()?.len ?? this.#settled;
    }
    if (len > this.#settled) freshen(node, this.#settled, this.#stamps, now);
  }
}

// Wraps each word past settled in a span whose fade started when the word
// appeared, so a word keeps its place in the fade when the block is redrawn.
// Formulas and drawings are left whole.
function freshen(node: HTMLElement, settled: number, stamps: { len: number; at: number }[], now: number): void {
  const walker = document.createTreeWalker(node, NodeFilter.SHOW_TEXT);
  const texts: Text[] = [];
  for (let n = walker.nextNode(); n; n = walker.nextNode()) texts.push(n as Text);
  let pos = 0;
  for (const text of texts) {
    const start = pos;
    pos += text.data.length;
    if (pos <= settled || text.parentElement?.closest(".katex, svg")) continue;
    const parts = document.createDocumentFragment();
    let at = start;
    for (const part of text.data.split(/(\s+)/)) {
      if (part === "") continue;
      if (at + part.length <= settled || /^\s+$/.test(part)) {
        parts.append(part);
      } else {
        const word = document.createElement("span");
        word.className = "fresh";
        word.textContent = part;
        const arrived = stamps.find((s) => s.len > at)?.at ?? now;
        word.style.animationDelay = `${Math.round(arrived - now)}ms`;
        parts.append(word);
      }
      at += part.length;
    }
    text.replaceWith(parts);
  }
}

/** Action for a scroller: while pinned says so, it stays at the end as what
    it holds grows, which a paced reveal does after its source has changed, and
    as the scroller itself is made shorter by something growing beside it. */
export function followEnd(node: HTMLElement, pinned: () => boolean) {
  const stick = () => {
    if (pinned()) node.scrollTop = node.scrollHeight;
  };
  const sizes = new ResizeObserver(stick);
  const watch = () => {
    sizes.disconnect();
    sizes.observe(node);
    for (const child of node.children) sizes.observe(child);
  };
  watch();
  const children = new MutationObserver(watch);
  children.observe(node, { childList: true });
  return {
    destroy() {
      sizes.disconnect();
      children.disconnect();
    },
  };
}
