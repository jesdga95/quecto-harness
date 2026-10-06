// The colour scheme. System follows the operating system, while Light and Dark
// are this browser's own choice, so they sit in local storage and no request
// carries them.

export type ThemePref = "system" | "light" | "dark";
export type Scheme = "light" | "dark";

const KEY = "quecto.theme";

const media = window.matchMedia("(prefers-color-scheme: dark)");

function stored(): ThemePref {
  try {
    const raw = localStorage.getItem(KEY);
    return raw === "light" || raw === "dark" ? raw : "system";
  } catch {
    return "system";
  }
}

function resolve(pref: ThemePref): Scheme {
  return pref === "system" ? (media.matches ? "dark" : "light") : pref;
}

export const theme = $state<{ pref: ThemePref; scheme: Scheme }>({
  pref: "system",
  scheme: "light",
});

/** Writes the resolved palette where the stylesheet reads it. */
function paint(): void {
  document.documentElement.dataset.scheme = theme.scheme;
}

export function setTheme(pref: ThemePref): void {
  theme.pref = pref;
  theme.scheme = resolve(pref);
  try {
    localStorage.setItem(KEY, pref);
  } catch {
    // The choice holds for this page.
  }
  paint();
}

theme.pref = stored();
theme.scheme = resolve(theme.pref);
paint();

// System means following the operating system while the page is open, not only
// at startup.
media.addEventListener("change", () => {
  if (theme.pref !== "system") return;
  theme.scheme = resolve(theme.pref);
  paint();
});
