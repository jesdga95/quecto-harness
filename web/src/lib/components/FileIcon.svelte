<script lang="ts">
  import File from "phosphor-svelte/lib/File";
  import FileCode from "phosphor-svelte/lib/FileCode";
  import FileImage from "phosphor-svelte/lib/FileImage";
  import FileText from "phosphor-svelte/lib/FileText";
  import { glyphOf } from "../files";

  // The same glyph a file link in a reply draws, so a file looks the same in
  // the reply, the panel and the row of changed files.
  let { path, size = 15 }: { path: string; size?: number } = $props();

  const glyph = $derived(glyphOf(path));
</script>

{#if glyph === "image"}
  <FileImage {size} class="shrink-0" />
{:else if glyph === "code"}
  <FileCode {size} class="shrink-0" />
{:else if glyph === "text"}
  <FileText {size} class="shrink-0" />
{:else}
  <File {size} class="shrink-0" />
{/if}
