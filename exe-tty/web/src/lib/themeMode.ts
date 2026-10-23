// Shared color-scheme handling for pages that expose an explicit
// Auto/Light/Dark control.
//
// PrimeVue is configured (by each app entry point) with
// `darkModeSelector: '.app-dark'`. We mirror that with `.app-light`
// for tokens.css overrides, and keep the class on <html> in sync with
// either the user's saved choice or — in Auto mode — the system
// preference.

export type ThemeMode = "auto" | "light" | "dark";

export interface ThemeOption {
  value: ThemeMode;
  // PrimeVue icon class (primeicons), e.g. 'pi-sun'.
  icon: string;
  title: string;
}

export const themeOptions: ThemeOption[] = [
  { value: "auto", icon: "pi-desktop", title: "Auto (match system)" },
  { value: "light", icon: "pi-sun", title: "Light" },
  { value: "dark", icon: "pi-moon", title: "Dark" },
];

// Where a page keeps its theme choice.
export interface ThemeStore {
  load(): ThemeMode;
  save(mode: ThemeMode): void;
}

export function loadThemeMode(key: string): ThemeMode {
  try {
    const v = localStorage.getItem(key);
    if (v === "light" || v === "dark" || v === "auto") return v;
  } catch {
    /* ignore (Safari private mode etc.) */
  }
  return "auto";
}

export function saveThemeMode(key: string, mode: ThemeMode): void {
  try {
    localStorage.setItem(key, mode);
  } catch {
    /* ignore */
  }
}

export function effectiveTheme(mode: ThemeMode): "light" | "dark" {
  if (mode === "light") return "light";
  if (mode === "dark") return "dark";
  return window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
}

export function applyDocumentTheme(eff: "light" | "dark"): void {
  const root = document.documentElement;
  root.classList.toggle("app-dark", eff === "dark");
  root.classList.toggle("app-light", eff === "light");
}

// Install a media-query listener that re-applies the document theme when
// the system preference changes *and* the current mode is 'auto'.
// `getMode` is read lazily so the caller can keep the ref up to date.
// Returns a teardown function.
export function watchSystemTheme(getMode: () => ThemeMode, onChange?: () => void): () => void {
  const mq = window.matchMedia("(prefers-color-scheme: dark)");
  const handler = () => {
    if (getMode() !== "auto") return;
    applyDocumentTheme(effectiveTheme("auto"));
    onChange?.();
  };
  mq.addEventListener("change", handler);
  return () => mq.removeEventListener("change", handler);
}
