<script lang="ts">
  import Composer from "./Composer.svelte";
  import ProjectTitle from "./ProjectTitle.svelte";
  import { app } from "../state/app.svelte";
  import { leave } from "../firstsend";
  import { shortPath } from "../format";

  let brace = $state<SVGSVGElement | null>(null);

  // One tall opening brace in the logo's geometry. The curls and the point
  // keep their size and only the straight runs stretch to the block's height.
  const K = 1.05;
  const STROKE = 4;
  const WIDTH = STROKE + 12 * K;
  let height = $state(0);
  const halves = $derived.by(() => {
    const H = height;
    const w = 6 * K, c = 7.8 * K, a = 3.3 * K;
    const xb = STROKE / 2, xm = xb + w, xe = xb + 2 * w;
    const yt = STROKE / 2, ym = H / 2, yb = H - STROKE / 2;
    return [
      `M${xb} ${ym}C${xb} ${ym} ${xm} ${ym - a} ${xm} ${ym - c}L${xm} ${yt + c}C${xm} ${yt + a} ${xm} ${yt} ${xe} ${yt}`,
      `M${xb} ${ym}C${xb} ${ym} ${xm} ${ym + a} ${xm} ${ym + c}L${xm} ${yb - c}C${xm} ${yb - a} ${xm} ${yb} ${xe} ${yb}`,
    ];
  });

  $effect(() => {
    const el = brace;
    if (!el) return;
    const measure = () => (height = Math.round(el.getBoundingClientRect().height));
    measure();
    const watch = new ResizeObserver(measure);
    watch.observe(el);
    return () => watch.disconnect();
  });
</script>

<div class="hero">
  <div class="hero-col">
    <div class="hero-project rise">
      <svg
        class="hero-brace"
        class:is-drawn={height > 0}
        viewBox="0 0 {WIDTH} {height || 1}"
        bind:this={brace}
        aria-hidden="true"
      >
        <path d={height > 0 ? halves[0] : ""} pathLength="1" />
        <path d={height > 0 ? halves[1] : ""} pathLength="1" />
      </svg>
      <ProjectTitle />
      {#if app.projectPath}
        <div class="hero-path num">{shortPath(app.projectPath)}</div>
      {/if}
    </div>

    <div class="rise mt-7">
      <Composer placeholder="Describe what you want to build" onbeforesend={leave} />
    </div>

    <!-- The keys belong to the empty screen, so they go with the first message. -->
    <div class="hero-keys rise">
      <span><kbd class="key">Enter</kbd>Send</span>
      <span><kbd class="key">Shift</kbd><kbd class="key">Enter</kbd>New line</span>
      <span><kbd class="key">Shift</kbd><kbd class="key">Tab</kbd>Switch mode</span>
      <span><kbd class="key">@</kbd>Quote a file</span>
    </div>
  </div>
</div>
