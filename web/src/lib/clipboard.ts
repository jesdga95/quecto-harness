// Copies text and reports whether it landed. The async clipboard exists only in
// a secure context, so a page served over plain http falls back to a selection.

/** How long a copy button shows its tick. */
export const COPIED_MS = 1500;

export async function copyText(text: string): Promise<boolean> {
  if (navigator.clipboard) {
    try {
      await navigator.clipboard.writeText(text);
      return true;
    } catch {
      // A denied permission or an unfocused document; try the selection.
    }
  }
  const area = document.createElement("textarea");
  area.value = text;
  area.setAttribute("readonly", "");
  area.style.position = "fixed";
  area.style.opacity = "0";
  document.body.append(area);
  area.select();
  try {
    return document.execCommand("copy");
  } catch {
    return false;
  } finally {
    area.remove();
  }
}
