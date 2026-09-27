<script lang="ts">
  // Flat, square, never recoloured, so the fills are literals rather than tokens.
  let {
    size = 26,
    wordmark = true,
    panel = false,
  }: { size?: number; wordmark?: boolean; panel?: boolean } = $props();

  // The braces carry the amber on the plate and on the dark panel alike.
  const stroke = "#FCD34D";
  // The plate's padding is part of the square mark. Without it the box is the
  // glyph's own bounds, so the mark sits close to whatever follows it.
  const box = $derived(panel ? "23.7 20.7 56.6 62.6" : "0 0 104 104");
  const height = $derived(panel ? Math.round((size * 62.6) / 56.6) : size);

  const type = $derived(Math.round(size * 0.47 * 10) / 10);
  const gap = $derived(Math.round(size * 0.27));
</script>

<span class="inline-flex items-center" style="gap:{gap}px">
  <svg
    width={size}
    height={height}
    viewBox={box}
    class="shrink-0"
    role={wordmark ? undefined : "img"}
    aria-label={wordmark ? undefined : "Quecto Harness"}
    aria-hidden={wordmark ? "true" : undefined}
  >
    {#if !panel}
      <rect width="104" height="104" rx="24" fill="#0D9488" />
    {/if}
    <path
      d="M40 25c-6 0-6 3.3-6 7.8v11.5c0 4.5-6 7.8-6 7.8s6 3.3 6 7.8v11.5c0 4.5 0 7.8 6 7.8"
      fill="none"
      stroke={stroke}
      stroke-width="8.6"
      stroke-linecap="round"
    />
    <path
      d="M64 25c6 0 6 3.3 6 7.8v11.5c0 4.5 6 7.8 6 7.8s-6 3.3-6 7.8v11.5c0 4.5 0 7.8-6 7.8"
      fill="none"
      stroke={stroke}
      stroke-width="8.6"
      stroke-linecap="round"
    />
  </svg>
  {#if wordmark}
    <span class="wordmark text-ink" style="font-size:{type}px">Quecto</span>
  {/if}
</span>
