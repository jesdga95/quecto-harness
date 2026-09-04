<script lang="ts">
  import type { ModelStatus } from "../api";
  let { status }: { status: ModelStatus } = $props();

  const label = $derived(status[0]!.toUpperCase() + status.slice(1));
</script>

<span
  class="dot"
  class:dot-loaded={status === "loaded"}
  class:dot-loading={status === "loading"}
  title={label}
></span>
