// One dialog at a time, asked for imperatively so a caller reads as a question:
// `if (await confirm({ ... })) remove()`.

export type ConfirmRequest = {
  title: string;
  body?: string;
  confirmLabel?: string;
  destructive?: boolean;
};

type Pending = ConfirmRequest & { resolve: (ok: boolean) => void };

export const dialog = $state<{ current: Pending | null }>({ current: null });

export function confirm(request: ConfirmRequest): Promise<boolean> {
  // A second question replaces the first, which answers no rather than hanging.
  dialog.current?.resolve(false);
  return new Promise<boolean>((resolve) => {
    dialog.current = { ...request, resolve };
  });
}

export function answer(ok: boolean): void {
  const pending = dialog.current;
  dialog.current = null;
  pending?.resolve(ok);
}
