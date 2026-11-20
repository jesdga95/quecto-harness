import { openFile } from "./state/app.svelte";

/** Opens the file a [data-file] button in the node names. The buttons are
    markup rather than components, so one listener answers for all of them. */
export function fileLinks(node: HTMLElement) {
  const click = (event: MouseEvent) => {
    const link = (event.target as HTMLElement).closest<HTMLElement>("[data-file]");
    if (!link || !node.contains(link)) return;
    event.preventDefault();
    const line = Number(link.dataset.line ?? 0);
    void openFile(link.dataset.file ?? "", Number.isFinite(line) ? line : 0);
  };
  node.addEventListener("click", click);
  return { destroy: () => node.removeEventListener("click", click) };
}
