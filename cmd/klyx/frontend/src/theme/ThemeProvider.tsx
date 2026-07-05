import { createContext, useContext, useEffect, useState, ReactNode } from "react";

export type Theme = "system" | "bench" | "bench-light" | "light" | "dark" | "midnight" | "graphite" | "crimson" | "forest" | "amber" | "violet";
export type EffectiveTheme = Exclude<Theme, "system">;
type Ctx = { theme: Theme; effectiveTheme: EffectiveTheme; setTheme: (theme: Theme) => void; toggle: () => void };

const ThemeContext = createContext<Ctx | null>(null);

const STORAGE_KEY = "klyx-theme";
const CHANGE_EVENT = "klyx:theme-change";
export const THEMES: ReadonlyArray<{ id: Theme; label: string }> = [
  { id: "system", label: "System" },
  { id: "bench", label: "Bench" },
  { id: "bench-light", label: "Bench Light" },
  { id: "light", label: "Light" },
  { id: "dark", label: "Dark" },
  { id: "midnight", label: "Midnight" },
  { id: "graphite", label: "Graphite" },
  { id: "crimson", label: "Crimson" },
  { id: "forest", label: "Forest" },
  { id: "amber", label: "Amber" },
  { id: "violet", label: "Violet" },
];

function isTheme(value: string | null): value is Theme {
  return value === "system" || value === "bench" || value === "bench-light" ||
    value === "light" || value === "dark" || value === "midnight" ||
    value === "graphite" || value === "crimson" || value === "forest" || value === "amber" || value === "violet";
}

const MIGRATED_KEY = "klyx-theme-bench-migrated";

function initial(): Theme {
  const saved = localStorage.getItem(STORAGE_KEY);
  if (!isTheme(saved)) return "bench";
  // One-time migration to direction C (2026-07-05): a palette saved before
  // the redesign maps onto its bench face ONCE - otherwise the redesign is
  // invisible to everyone with a stored theme. Any choice made after this
  // sticks permanently (the flag is never consulted again).
  if (!localStorage.getItem(MIGRATED_KEY)) {
    localStorage.setItem(MIGRATED_KEY, "1");
    if (saved === "light") return "bench-light";
    if (saved !== "bench" && saved !== "bench-light" && saved !== "system") return "bench";
  }
  return saved;
}

function prefersDark(): boolean {
  return typeof window !== "undefined" && typeof window.matchMedia === "function"
    ? window.matchMedia("(prefers-color-scheme: dark)").matches
    : false;
}

export function resolveTheme(theme: Theme): EffectiveTheme {
  // "system" follows the OS into the bench faces - direction C is the app's
  // look; the OS only chooses dark or light.
  return theme === "system" ? (prefersDark() ? "bench" : "bench-light") : theme;
}

function applyTheme(theme: Theme): EffectiveTheme {
  const effective = resolveTheme(theme);
  document.documentElement.setAttribute("data-theme-choice", theme);
  document.documentElement.setAttribute("data-theme", effective);
  document.documentElement.style.colorScheme = effective === "light" || effective === "bench-light" ? "light" : "dark";
  localStorage.setItem(STORAGE_KEY, theme);
  return effective;
}

export function setThemeChoice(theme: Theme): EffectiveTheme {
  const effective = applyTheme(theme);
  window.dispatchEvent(new CustomEvent(CHANGE_EVENT, { detail: { theme } }));
  return effective;
}

// toggleTheme cycles concrete themes using the same storage key and DOM
// attribute the provider manages, for callers outside the React tree (e.g. the
// command palette). System remains an explicit menu choice; the quick action
// moves through the concrete palettes.
export function toggleTheme(): Theme {
  const stored = document.documentElement.getAttribute("data-theme-choice") ?? localStorage.getItem(STORAGE_KEY);
  const current: Theme = isTheme(stored) ? stored : "bench";
  const order: Theme[] = ["bench", "bench-light", "light", "dark", "midnight", "graphite", "crimson", "forest", "amber", "violet"];
  const next = order[(Math.max(0, order.indexOf(current)) + 1) % order.length];
  setThemeChoice(next);
  return next;
}

export function ThemeProvider({ children }: { children: ReactNode }) {
  const [theme, setTheme] = useState<Theme>(initial);
  const [effectiveTheme, setEffectiveTheme] = useState<EffectiveTheme>(() => resolveTheme(initial()));

  useEffect(() => {
    setEffectiveTheme(applyTheme(theme));
  }, [theme]);

  const toggle = () => setTheme((t) => {
    const order: Theme[] = ["bench", "bench-light", "light", "dark", "midnight", "graphite", "crimson", "forest", "amber", "violet"];
    return order[(Math.max(0, order.indexOf(t)) + 1) % order.length];
  });
  const choose = (next: Theme) => setTheme(next);

  useEffect(() => {
    const onThemeChange = (e: Event) => {
      const next = (e as CustomEvent<{ theme?: Theme }>).detail?.theme;
      if (next && isTheme(next)) setTheme(next);
    };
    const media = window.matchMedia?.("(prefers-color-scheme: dark)");
    const onMediaChange = () => {
      if (theme === "system") setEffectiveTheme(applyTheme("system"));
    };
    window.addEventListener(CHANGE_EVENT, onThemeChange);
    media?.addEventListener?.("change", onMediaChange);
    return () => {
      window.removeEventListener(CHANGE_EVENT, onThemeChange);
      media?.removeEventListener?.("change", onMediaChange);
    };
  }, [theme]);

  return <ThemeContext.Provider value={{ theme, effectiveTheme, setTheme: choose, toggle }}>{children}</ThemeContext.Provider>;
}

export function useTheme(): Ctx {
  const c = useContext(ThemeContext);
  if (!c) throw new Error("useTheme must be used within ThemeProvider");
  return c;
}
