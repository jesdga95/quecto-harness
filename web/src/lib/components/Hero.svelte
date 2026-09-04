<script lang="ts">
  import Composer from "./Composer.svelte";
  import Logo from "./Logo.svelte";
  import ProjectChip from "./ProjectChip.svelte";

</script>

<div class="flex min-h-0 flex-1 flex-col items-center justify-center px-6">
  <div class="flex w-full max-w-[720px] flex-col items-center">
    <Logo size={128} wordmark={false} bare />
    <h1 class="mt-7 text-[27px] font-medium">Ready when you are</h1>

    <div class="mt-5">
      <ProjectChip inline big />
    </div>

    <div class="mt-7 w-full">
      <Composer placeholder="Describe what you want to build" />
    </div>
  </div>
</div>
