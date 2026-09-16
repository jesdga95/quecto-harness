<script lang="ts">
  import X from "phosphor-svelte/lib/X";
  import type { Config, PermMode } from "../api";
  import { app, confirmFullAuto, removeAllowed } from "../state/app.svelte";
  import OptionPicker from "./OptionPicker.svelte";

  let {
    config,
    onchange,
  }: {
    config: Config;
    onchange: (patch: Partial<Config>) => void;
  } = $props();

  const MODES: { value: PermMode; label: string; hint: string }[] = [
    { value: "ask", label: "Ask", hint: "Every tool call is shown before it runs." },
    {
      value: "allowlist",
      label: "Allowlist",
      hint: "Writes and allowlisted commands run on their own; the rest ask.",
    },
    { value: "full", label: "Full auto", hint: "Anything runs, anywhere, unreviewed." },
  ];

  // The warning is asked at the moment full is picked, before it lands in the
  // draft, keeping the confirm the composer's chip has.
  async function pick(mode: PermMode) {
    if (mode === config.permissions) return;
    if (mode === "full" && !(await confirmFullAuto())) return;
    onchange({ permissions: mode });
  }

  export function flush() {}
</script>

<div class="field-card">
  <h4 class="field-head">Policy</h4>
  <div class="flex flex-col gap-1.5">
    <span class="label">Permission mode</span>
    <OptionPicker
      value={config.permissions}
      options={MODES}
      onpick={(m) => void pick(m)}
      label="Permission mode"
    />
  </div>
</div>

<div class="field-card">
  <div class="flex flex-col gap-2">
    <h4 class="field-head">Allowlist for this project</h4>
    {#each app.allowlist as prefix (prefix)}
      <div class="flex items-center gap-2">
        <span class="mono-block flex-1 !py-1.5">{prefix}</span>
        <button class="icon-btn" aria-label="Remove {prefix}" onclick={() => removeAllowed(prefix)}>
          <X />
        </button>
      </div>
    {:else}
      <p class="text-[12.5px] text-muted">
        Nothing allowed yet. A command prefix added from a permission card lands here.
      </p>
    {/each}
  </div>
</div>
