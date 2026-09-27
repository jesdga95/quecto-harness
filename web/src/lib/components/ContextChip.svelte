<script lang="ts">
  import ArrowsIn from "phosphor-svelte/lib/ArrowsIn";
  import Popover from "./Popover.svelte";
  import { app, compact, turnStats } from "../state/app.svelte";
  import { duration, k, level, pct } from "../format";

  let open = $state(false);

  const fill = $derived(app.usage.prompt_tokens + app.usage.completion_tokens);
  // A window nothing reported and nothing overrode is no limit, so the chip
  // counts the tokens rather than a share of a window.
  const limited = $derived(app.usage.n_ctx > 0);
  const ratio = $derived(limited ? fill / app.usage.n_ctx : 0);
  const percent = $derived(pct(fill, app.usage.n_ctx));
  const tone = $derived(level(ratio));
  const running = $derived(app.status !== "idle");

  // The parts are scaled against the whole window, so each segment is its
  // own share of it and the tail of the bar is what is still free. They
  // arrive once the first turn has run, so the bar is plain until then.
  // With no window they are scaled against the fill, so the bar reads as
  // the composition of the prompt with nothing left free.
  // The system, agents and tools lines are character-count estimates, so
  // they carry a tilde; messages is the remainder of a real usage report.
  const parts = $derived(app.usage.breakdown);
  const share = (n: number) => pct(n, limited ? app.usage.n_ctx : fill);

  // The line the bar fills toward. The tick only speaks where a window is,
  // and a label at the very edge would overhang the popover.
  const compactAt = $derived(app.config?.compact_at ?? 0.8);
  const hasTick = $derived(limited && compactAt > 0 && compactAt < 1);
  const tickPct = $derived(compactAt * 100);
  const tickSide = $derived(hasTick ? (tickPct <= 10 ? "start" : tickPct >= 90 ? "end" : "mid") : "");

  // The context as it stood at each finished turn's last request, so the
  // line climbs toward the window rather than billing. A turn stored before
  // the figure was counted is left out.
  const series = $derived(
    app.items
      .map((i) => (i.kind === "assistant" && i.cost?.window ? i.cost.window : undefined))
      .filter((n): n is number => n !== undefined),
  );

  // Round up to a 1, 2, 2.5, 5 or 10 of a power of ten, so the axis values
  // are clean.
  const niceCeil = (v: number) => {
    const mag = Math.pow(10, Math.floor(Math.log10(Math.max(v, 1))));
    for (const m of [1, 2, 2.5, 5, 10]) if (m * mag >= v) return m * mag;
    return 10 * mag;
  };

  // The line scales to its own data, so a gentle climb still reads as a
  // climb; the tick on the bar above is the threshold.
  const spark = $derived.by(() => {
    if (series.length < 2) return null;
    const max = niceCeil(Math.max(...series));
    const y = (v: number) => 58 - (v / max) * 46;
    const pts = series.map((v, i) => {
      const x = 4 + (i * 264) / (series.length - 1);
      return [Math.round(x * 10) / 10, Math.round(y(v) * 10) / 10] as const;
    });
    const line = pts.map(([x, v]) => `${x},${v}`).join(" ");
    return {
      line,
      area: `${line} 268,58 4,58`,
      points: pts,
      grid: [max / 2, max].map((v) => ({ v, y: Math.round(y(v) * 10) / 10 })),
    };
  });

  // The session so far, summed from what each finished turn recorded. A turn
  // stored before a field was counted adds nothing to it, and a figure no turn
  // has is left out rather than shown as zero.
  const sums = $derived.by(() => {
    const s = {
      turns: 0,
      output: 0,
      input: 0,
      inputSeen: false,
      cached: 0,
      cachedSeen: false,
      llm: 0,
      tool: 0,
      timed: false,
      first: 0,
      firstN: 0,
      gen: 0,
    };
    for (const item of app.items) {
      if (item.kind !== "assistant" || !item.cost) continue;
      const c = item.cost;
      s.turns += 1;
      s.output += c.tokens;
      if (c.input !== undefined) {
        s.input += c.input;
        s.inputSeen = true;
      }
      if (c.cached !== undefined) {
        s.cached += c.cached;
        s.cachedSeen = true;
      }
      if (c.llmMs !== undefined) {
        s.llm += c.llmMs;
        s.tool += c.toolMs ?? 0;
        s.gen += c.genMs ?? 0;
        s.timed = true;
      }
      if (c.firstMs !== undefined && c.requests > 0) {
        s.first += c.firstMs;
        s.firstN += c.requests;
      }
    }
    return s;
  });

  const stats = $derived(turnStats());
  const tps = $derived(sums.gen > 0 ? Math.round(sums.output / (sums.gen / 1000)) : 0);
  const total = $derived(sums.input + sums.output);
  const hit = $derived(
    sums.cachedSeen && sums.input > 0 ? Math.round((sums.cached / sums.input) * 100) : undefined,
  );

  // Circumference of the r=7 track, so the dash offset is a percentage of it.
  const circumference = 2 * Math.PI * 7;
  const dash = $derived((percent / 100) * circumference);
</script>

<div class="relative">
  <button
    class="chip chip-solid !gap-1.5 !py-1 !pr-2.5 !pl-1.5"
    class:is-warn={tone === "warn"}
    class:is-crit={tone === "crit"}
    onclick={() => (open = !open)}
    title="Context window"
  >
    <svg viewBox="0 0 18 18" class="!h-[18px] !w-[18px] shrink-0 -rotate-90">
      <circle cx="9" cy="9" r="7" fill="none" stroke="var(--border)" stroke-width="2.5" />
      <circle
        cx="9"
        cy="9"
        r="7"
        fill="none"
        stroke="currentColor"
        stroke-width="2.5"
        stroke-linecap="round"
        stroke-dasharray="{dash} {circumference}"
      />
    </svg>
    <span class="num text-[12px]">{limited ? `${percent}%` : k(fill)}</span>
  </button>

  {#if open}
    <Popover onclose={() => (open = false)} align="right" width={288}>
      <div class="px-2.5 py-2">
        <div class="flex items-baseline justify-between">
          <span class="text-[13px] font-medium">Context</span>
          <span class="num text-[12px] text-muted">
            {#if limited}{k(fill)} / {k(app.usage.n_ctx)}{:else}{k(fill)} · No limit{/if}
          </span>
        </div>

        {#if parts}
          <div class="ctxline" class:has-tick={hasTick}>
            <div class="bar">
              <span class="seg-system" style="width:{share(parts.system)}%"></span>
              <span class="seg-rules" style="width:{share(parts.rules)}%"></span>
              <span class="seg-tools" style="width:{share(parts.tools)}%"></span>
              <span class="seg-msg" style="width:{share(parts.messages)}%"></span>
            </div>
            {#if hasTick}
              <span class="tick" style="left:{tickPct}%"></span>
              <span
                class="tick-label"
                class:is-start={tickSide === "start"}
                class:is-end={tickSide === "end"}
                style="left:{tickPct}%"
              >
                Compact {k(Math.round(app.usage.n_ctx * compactAt))}
              </span>
            {/if}
          </div>

          <dl class="ctxlegend">
            <div class="row">
              <dt class="flex items-center gap-1.5"><span class="ctxdot seg-system"></span>System
                prompt</dt>
              <dd class="num">~{k(parts.system)}</dd>
            </div>
            <div class="row">
              <dt class="flex items-center gap-1.5" title="AGENTS.md in the project root"
                ><span class="ctxdot seg-rules"></span>Agents</dt
              >
              <dd class="num">~{k(parts.rules)}</dd>
            </div>
            <div class="row">
              <dt class="flex items-center gap-1.5"><span class="ctxdot seg-tools"></span>Tools</dt>
              <dd class="num">~{k(parts.tools)}</dd>
            </div>
            <div class="row">
              <dt class="flex items-center gap-1.5"><span class="ctxdot seg-msg"></span>Messages</dt>
              <dd class="num">{k(parts.messages)}</dd>
            </div>
          </dl>
        {:else}
          <div class="ctxline" class:has-tick={hasTick}>
            <div class="bar">
              <span class="seg-plain" style="width:{limited ? percent : 100}%"></span>
            </div>
            {#if hasTick}
              <span class="tick" style="left:{tickPct}%"></span>
              <span
                class="tick-label"
                class:is-start={tickSide === "start"}
                class:is-end={tickSide === "end"}
                style="left:{tickPct}%"
              >
                Compact {k(Math.round(app.usage.n_ctx * compactAt))}
              </span>
            {/if}
          </div>
        {/if}

        {#if spark}
          <div class="ctxspark">
            <svg viewBox="0 0 280 58" preserveAspectRatio="none">
              {#each spark.grid as g}
                <line x1="0" y1={g.y} x2="280" y2={g.y} stroke="var(--hair)" />
                <text x="2" y={g.y - 3} font-size="9" fill="var(--muted)" class="num">
                  {k(g.v)}
                </text>
              {/each}
              <polygon points={spark.area} fill="var(--c-msg)" opacity="0.15" />
              <polyline points={spark.line} fill="none" stroke="var(--c-msg)" stroke-width="2" />
              {#each spark.points as p, i}
                <circle cx={p[0]} cy={p[1]} r={i === spark.points.length - 1 ? 2.5 : 1.75} fill="var(--c-msg)" />
              {/each}
            </svg>
          </div>
        {/if}

        {#if sums.turns > 0}
          <div class="ctx-session">
            <div class="stat-title">This session</div>
            <div class="stat-tiles">
              <div><span>Tokens</span><b class="num">{k(total)}</b></div>
              {#if hit !== undefined}
                <div><span>From cache</span><b class="num">{hit}%</b></div>
              {:else}
                <div><span>Generated</span><b class="num">{k(sums.output)}</b></div>
              {/if}
              {#if tps > 0}
                <div><span>Speed</span><b class="num">{tps}<small> tok/s</small></b></div>
              {/if}
              {#if sums.firstN > 0}
                <div><span>First token</span><b class="num">{duration(sums.first / sums.firstN)}</b></div>
              {/if}
            </div>
            <p class="stat-note">
              {stats.turns} {stats.turns === 1 ? "turn" : "turns"}, {stats.steps} steps{#if sums.timed}. {duration(sums.llm)} on the model, {duration(sums.tool)} on tools{/if}
            </p>
          </div>
        {/if}

        <button
          class="chip chip-plain mt-3 w-full justify-center"
          onclick={() => {
            open = false;
            void compact();
          }}
          disabled={running || !app.usage.compactable}
        >
          <ArrowsIn /> Compact now
        </button>
        {#if !running && !app.usage.compactable}
          <p class="mt-1.5 text-center text-[12px] text-muted">Not enough content to compact yet</p>
        {/if}
      </div>
    </Popover>
  {/if}
</div>
