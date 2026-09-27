<script lang="ts">
  import { app } from "../state/app.svelte";

  let layer = $state<HTMLDivElement | null>(null);

  // The light leans toward the pointer along the x axis. Only its layers move,
  // and only until they catch up, so the blur is never redrawn.
  $effect(() => {
    const el = layer;
    if (!el || matchMedia("(prefers-reduced-motion: reduce)").matches) return;
    let at = 0;
    let target = 0;
    let frame = 0;
    const step = () => {
      at += (target - at) * 0.06;
      const done = Math.abs(target - at) < 0.3;
      if (done) at = target;
      for (const band of el.querySelectorAll<HTMLElement>(".glow-band")) {
        band.style.translate = `${(at * 0.35).toFixed(1)}px 0`;
      }
      for (const pool of el.querySelectorAll<HTMLElement>(".glow-pool")) {
        pool.style.translate = `${(at * 0.7).toFixed(1)}px 0`;
      }
      frame = done ? 0 : requestAnimationFrame(step);
    };
    const move = (e: PointerEvent) => {
      const box = el.getBoundingClientRect();
      target = e.clientX - (box.left + box.width / 2);
      if (!frame) frame = requestAnimationFrame(step);
    };
    window.addEventListener("pointermove", move);
    return () => {
      window.removeEventListener("pointermove", move);
      cancelAnimationFrame(frame);
    };
  });
</script>

<div class="glow" class:is-plan={app.workMode === "plan"} bind:this={layer} aria-hidden="true">
  <div class="glow-set glow-exec"><i class="glow-band"></i><i class="glow-pool"></i></div>
  <div class="glow-set glow-plan"><i class="glow-band"></i><i class="glow-pool"></i></div>
</div>
