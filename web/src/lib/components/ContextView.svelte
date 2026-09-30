<script lang="ts">
  import ArrowsIn from "phosphor-svelte/lib/ArrowsIn";
  import ChartDonut from "phosphor-svelte/lib/ChartDonut";
  import type { TurnCost } from "../api";
  import { app, compact, contextUse, turnStats } from "../state/app.svelte";
  import { duration, k, level } from "../format";

  const use = $derived(contextUse());
  const tone = $derived(level(use.ratio));
  const running = $derived(app.status !== "idle");
  const compactAt = $derived(app.config?.compact_at ?? 0.8);
  const hasTick = $derived(use.limited && compactAt > 0 && compactAt < 1);
  const threshold = $derived(hasTick ? Math.round(app.usage.n_ctx * compactAt) : 0);

  // The parts are measured against the whole window, so the tail of the bar is
  // what is still free. Without a window they are shares of the fill. System,
  // agents and tools are estimates from their text, so they carry a tilde.
  const parts = $derived(app.usage.breakdown);
  const whole = $derived(use.limited ? app.usage.n_ctx : use.fill);
  const share = (n: number) => (whole > 0 ? (n / whole) * 100 : 0);
  const rows = $derived(
    parts
      ? [
          { label: "System prompt", cls: "seg-system", n: parts.system, estimate: true },
          { label: "Agents", cls: "seg-rules", n: parts.rules, estimate: true },
          { label: "Tools", cls: "seg-tools", n: parts.tools, estimate: true },
          { label: "Messages", cls: "seg-msg", n: parts.messages, estimate: false },
        ]
      : [],
  );

  // Every finished turn, oldest first, with what it recorded.
  const turns = $derived(
    app.items.flatMap((i) => (i.kind === "assistant" && i.cost ? [i.cost] : [])) as TurnCost[],
  );
  const last = $derived(turns.at(-1));

  const sums = $derived.by(() => {
    const s = { output: 0, input: 0, inputSeen: false, cached: 0, cachedSeen: false, requests: 0 };
    const t = { timed: false, llm: 0, gen: 0, tool: 0, first: 0, firstN: 0 };
    for (const c of turns) {
      s.output += c.tokens;
      s.requests += c.requests;
      if (c.input !== undefined) {
        s.input += c.input;
        s.inputSeen = true;
      }
      if (c.cached !== undefined) {
        s.cached += c.cached;
        s.cachedSeen = true;
      }
      if (c.llmMs !== undefined) {
        t.timed = true;
        t.llm += c.llmMs;
        t.gen += c.genMs ?? 0;
        t.tool += c.toolMs ?? 0;
      }
      if (c.firstMs !== undefined && c.requests > 0) {
        t.first += c.firstMs;
        t.firstN += c.requests;
      }
    }
    return { ...s, ...t };
  });
  const stats = $derived(turnStats());
  const tps = $derived(sums.gen > 0 ? Math.round(sums.output / (sums.gen / 1000)) : 0);
  const hit = $derived(
    sums.cachedSeen && sums.input > 0 ? Math.round((sums.cached / sums.input) * 100) : undefined,
  );
  const tiles = $derived(
    [
      ["Tokens", k(sums.input + sums.output)],
      sums.inputSeen ? ["Input", k(sums.input)] : null,
      ["Generated", k(sums.output)],
      hit !== undefined ? ["From cache", `${hit}%`] : null,
      tps > 0 ? ["Speed", `${tps} tok/s`] : null,
      sums.firstN > 0 ? ["First token", duration(sums.first / sums.firstN)] : null,
      ["Requests", String(sums.requests)],
      sums.timed ? ["Model time", duration(sums.llm)] : null,
      sums.timed ? ["Tool time", duration(sums.tool)] : null,
    ].filter((t): t is string[] => t !== null),
  );

  // Time split three ways: waiting for the first token, generating, tools.
  const spent = $derived(sums.first + sums.gen + sums.tool);
  const time = $derived(
    sums.timed && spent > 0
      ? [
          { label: "Waiting for the first token", cls: "seg-system", ms: sums.first },
          { label: "Generating", cls: "seg-msg", ms: sums.gen },
          { label: "Tools", cls: "seg-tools", ms: sums.tool },
        ]
      : [],
  );

  // Round up to a 1, 2, 2.5, 5 or 10 of a power of ten, so the axis is clean.
  const niceCeil = (v: number) => {
    const mag = Math.pow(10, Math.floor(Math.log10(Math.max(v, 1))));
    for (const m of [1, 2, 2.5, 5, 10]) if (m * mag >= v) return m * mag;
    return 10 * mag;
  };

  // The context at each turn's last request. With a window the scale reaches
  // the compaction line, so the headroom reads true. The chart is drawn at the
  // width it is given, so its text and points keep their shape.
  let width = $state(0);
  const W = $derived(Math.max(width, 200));
  const H = 150;
  const LEFT = 34;
  const TOP = 12;
  const BOTTOM = 22;
  const chart = $derived.by(() => {
    const series = turns.map((c, i) => ({ turn: i + 1, v: c.window })).filter((p) => p.v !== undefined) as {
      turn: number;
      v: number;
    }[];
    if (series.length < 2) return null;
    const max = niceCeil(Math.max(...series.map((p) => p.v), threshold));
    const y = (v: number) => TOP + (1 - v / max) * (H - TOP - BOTTOM);
    const x = (i: number) => LEFT + (i * (W - LEFT - 8)) / (series.length - 1);
    const points = series.map((p, i) => ({ x: x(i), y: y(p.v), turn: p.turn, v: p.v }));
    const line = points.map((p) => `${p.x},${p.y}`).join(" ");
    const step = Math.ceil(series.length / 12);
    return {
      line,
      area: `${line} ${x(series.length - 1)},${y(0)} ${LEFT},${y(0)}`,
      points,
      labels: points.filter((_, i) => i % step === 0 || i === points.length - 1),
      grid: [0, 0.25, 0.5, 0.75, 1].map((f) => ({ v: max * f, y: y(max * f) })),
      tick: threshold > 0 ? y(threshold) : null,
    };
  });

  function average(c: TurnCost): string {
    return c.firstMs !== undefined && c.requests > 0 ? duration(c.firstMs / c.requests) : "";
  }
</script>

{#if !use.measured}
  <div class="panel-path">
    <div class="panel-crumbs"><span class="crumb">Not measured</span></div>
  </div>
  <div class="dock-empty">
    <div class="dock-empty-icon"><ChartDonut size={21} /></div>
    <h3>Nothing measured yet</h3>
    <p>How the context fills, and what each turn cost, shows here once the first message has gone.</p>
  </div>
{:else}
  <div class="artifact-body">
    <div class="ctxv min-h-0 flex-1 overflow-y-auto">
      <section>
        <div class="ctxv-big" class:is-warn={tone === "warn"} class:is-crit={tone === "crit"}>
          <b class="num">{use.limited ? `${use.percent}%` : k(use.fill)}</b>
          <span class="num">
            {#if use.limited}{k(use.fill)} of {k(app.usage.n_ctx)} tokens{:else}tokens, no window known{/if}
          </span>
        </div>
        <p class="ctxv-sub">
          {#if last?.model}{last.model}{#if last.agent} on {last.agent}{/if}.{/if}
          {#if threshold > 0}
            Compacts at {Math.round(compactAt * 100)}%{#if threshold > use.fill}, {k(threshold - use.fill)} tokens from now{/if}.
          {/if}
        </p>
      </section>

      <section>
        <h4>What fills it</h4>
        <div class="ctxline" class:has-tick={hasTick}>
          <div class="bar">
            {#if parts}
              {#each rows as row (row.label)}
                <span class={row.cls} style="width:{share(row.n)}%"></span>
              {/each}
            {:else}
              <span class="seg-plain" style="width:{use.limited ? use.percent : 100}%"></span>
            {/if}
          </div>
          {#if hasTick}
            <span class="tick" style="left:{compactAt * 100}%"></span>
            <span class="tick-label" style="left:{compactAt * 100}%">Compact {k(threshold)}</span>
          {/if}
        </div>
        {#if parts}
          <table class="ctxv-parts" class:has-tick={hasTick}>
            <tbody>
              {#each rows as row (row.label)}
                <tr>
                  <td><span class="ctxdot {row.cls}"></span>{row.label}</td>
                  <td class="num">{row.estimate ? "~" : ""}{k(row.n)}</td>
                  <td class="num">{share(row.n).toFixed(1)}%</td>
                </tr>
              {/each}
              {#if use.limited}
                <tr class="is-free">
                  <td><span class="ctxdot is-free"></span>Free</td>
                  <td class="num">{k(Math.max(0, app.usage.n_ctx - use.fill))}</td>
                  <td class="num">{Math.max(0, 100 - share(use.fill)).toFixed(1)}%</td>
                </tr>
              {/if}
            </tbody>
          </table>
        {/if}
      </section>

      {#if chart}
        <section>
          <h4>Context by turn<span>At each turn's last request</span></h4>
          <div bind:clientWidth={width}>
          <svg class="ctxv-chart" viewBox="0 0 {W} {H}" width={W} height={H} role="img" aria-label="Context by turn">
            {#each chart.grid as g (g.v)}
              <line x1={LEFT} x2={W} y1={g.y} y2={g.y} stroke="var(--hair)" />
              <text x="0" y={g.y + 3} class="num">{k(Math.round(g.v))}</text>
            {/each}
            {#if chart.tick !== null}
              <line x1={LEFT} x2={W} y1={chart.tick} y2={chart.tick} class="ctxv-threshold" />
            {/if}
            <polygon points={chart.area} fill="var(--c-msg)" opacity="0.14" />
            <polyline points={chart.line} fill="none" stroke="var(--c-msg)" stroke-width="2" />
            {#each chart.points as p, i (p.turn)}
              <circle cx={p.x} cy={p.y} r={i === chart.points.length - 1 ? 3 : 2} fill="var(--c-msg)">
                <title>Turn {p.turn}: {k(p.v)}</title>
              </circle>
            {/each}
            {#each chart.labels as p (p.turn)}
              <text x={p.x} y={H - 6} text-anchor="middle" class="num">{p.turn}</text>
            {/each}
          </svg>
          </div>
        </section>
      {/if}

      {#if turns.length > 0}
        <section>
          <h4>This session<span>{stats.turns} {stats.turns === 1 ? "turn" : "turns"}, {stats.steps} steps</span></h4>
          <div class="ctxv-tiles">
            {#each tiles as [label, value] (label)}
              <div><span>{label}</span><b class="num">{value}</b></div>
            {/each}
          </div>
        </section>
      {/if}

      {#if time.length > 0}
        <section>
          <h4>Where the time went<span class="num">{duration(spent)}</span></h4>
          <div class="ctxv-time">
            {#each time as part (part.label)}
              <span class={part.cls} style="width:{(part.ms / spent) * 100}%"></span>
            {/each}
          </div>
          <div class="ctxv-legend">
            {#each time as part (part.label)}
              <span><span class="ctxdot {part.cls}"></span>{part.label}<b class="num">{duration(part.ms)}</b></span>
            {/each}
          </div>
        </section>
      {/if}

      {#if turns.length > 0}
        <section>
          <h4>By turn<span>Newest first</span></h4>
          <div class="ctxv-table">
            <table class="ctxv-turns">
              <thead>
                <tr>
                  <th>Turn</th>
                  <th>Context</th>
                  <th>Input</th>
                  <th>Cached</th>
                  <th>Generated</th>
                  <th>tok/s</th>
                  <th>First</th>
                  <th>Requests</th>
                  <th>Took</th>
                </tr>
              </thead>
              <tbody>
                {#each [...turns].reverse() as c, i (i)}
                  <tr>
                    <td>Turn {turns.length - i}</td>
                    <td class="num">{c.window !== undefined ? k(c.window) : ""}</td>
                    <td class="num">{c.input !== undefined ? k(c.input) : ""}</td>
                    <td class="num">
                      {c.cached !== undefined && c.input ? `${Math.round((c.cached / c.input) * 100)}%` : ""}
                    </td>
                    <td class="num">{k(c.tokens)}</td>
                    <td class="num">{c.tokPerS > 0 ? Math.round(c.tokPerS) : ""}</td>
                    <td class="num">{average(c)}</td>
                    <td class="num">{c.requests}</td>
                    <td class="num">{duration(c.ms)}</td>
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>
        </section>
      {/if}
    </div>

    <div class="ctxv-foot">
      <button
        class="ctxv-compact"
        onclick={() => void compact()}
        disabled={running || !app.usage.compactable}
        title={!running && !app.usage.compactable ? "Not enough content to compact yet" : undefined}
      >
        <ArrowsIn size={15} />Compact now
      </button>
    </div>
  </div>
{/if}
