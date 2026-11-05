// Passages picked from a reply, waiting in the composer for the next message.
// The popover over the transcript adds them and the composer sends them.

export const quoted = $state<{ items: string[]; added: number }>({ items: [], added: 0 });

export function addQuote(text: string): void {
  const clean = text.trim();
  if (!clean) return;
  quoted.items = [...quoted.items, clean];
  quoted.added++;
}

export function removeQuote(index: number): void {
  quoted.items = quoted.items.filter((_, i) => i !== index);
}

export function takeQuotes(): string[] {
  const items = quoted.items;
  quoted.items = [];
  return items;
}
