// The colour palette. Following the operating system is a choice of its own from
// which palette to paint when it is not followed, so both are remembered. They
// sit in local storage and no request carries them.

// The four the operating system can decide between, and the two a reader picks
// by hand. No contrast or colour setting resolves to one of the last two.
export type Palette =
  | "light"
  | "dark"
  | "high-contrast-light"
  | "high-contrast-dark"
  | "grey-paper"
  | "cream";

const PALETTE_KEY = "quecto.theme";
const FOLLOW_KEY = "quecto.follow";

const scheme = window.matchMedia("(prefers-color-scheme: dark)");
const contrast = window.matchMedia("(prefers-contrast: more)");

function isPalette(value: unknown): value is Palette {
  return (
    value === "light" ||
    value === "dark" ||
    value === "high-contrast-light" ||
    value === "high-contrast-dark" ||
    value === "grey-paper" ||
    value === "cream"
  );
}

function storedPalette(): Palette {
  try {
    const raw = localStorage.getItem(PALETTE_KEY);
    return isPalette(raw) ? raw : "light";
  } catch {
    return "light";
  }
}

// Following the operating system is the default, so only a stored "0" stops it.
function storedFollow(): boolean {
  try {
    return localStorage.getItem(FOLLOW_KEY) !== "0";
  } catch {
    return true;
  }
}

/** The palette the operating system asks for. Its contrast setting picks the
    pair and its colour scheme picks which of the two. */
function fromSystem(): Palette {
  if (scheme.matches) return contrast.matches ? "high-contrast-dark" : "dark";
  return contrast.matches ? "high-contrast-light" : "light";
}

export const theme = $state<{
  follow: boolean;
  /** The palette asked for when the operating system is not followed. */
  palette: Palette;
  /** The palette in force, which is the operating system's while it is followed. */
  active: Palette;
}>({ follow: true, palette: "light", active: "light" });

/** Writes the palette in force where the stylesheet reads it. */
function paint(): void {
  document.documentElement.dataset.scheme = theme.active;
}

function resolve(): void {
  theme.active = theme.follow ? fromSystem() : theme.palette;
  paint();
}

function remember(key: string, value: string): void {
  try {
    localStorage.setItem(key, value);
  } catch {
    // The choice holds for this page.
  }
}

export function setFollow(follow: boolean): void {
  theme.follow = follow;
  remember(FOLLOW_KEY, follow ? "1" : "0");
  resolve();
}

/** Choosing a palette is what stops following the operating system. */
export function setPalette(palette: Palette): void {
  theme.palette = palette;
  theme.follow = false;
  remember(PALETTE_KEY, palette);
  remember(FOLLOW_KEY, "0");
  resolve();
}

theme.follow = storedFollow();
theme.palette = storedPalette();
resolve();

// Following the operating system means following it while the page is open, not
// only at startup, and its contrast setting counts as much as its colour scheme.
for (const media of [scheme, contrast]) {
  media.addEventListener("change", () => {
    if (theme.follow) resolve();
  });
}
