<script lang="ts">
  import type { ModelStatus } from "../api";
  let { status }: { status: ModelStatus } = $props();

  const label = $derived(status[0]!.toUpperCase() + status.slice(1));
</script>

<!-- A model on its way in turns the dot into a ring that turns, so the one
     place that names the model is also where its state is read. -->
{#if status === "loading"}
  <span class="dot-spin" title={label}></span>
{:else}
  <span class="dot" class:dot-loaded={status === "loaded"} title={label}></span>
{/if}
