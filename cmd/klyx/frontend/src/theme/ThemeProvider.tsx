import { createContext, useContext, useEffect, useState, ReactNode } from "react";

// The Line (2026-10-14): night and day are the app's look. Light and dark stay
// as "classic"; bench and bench-light remain selectable for one release after
// the migration (owner decision 1), and the six palettes are gone.
export type Theme = "system" | "night" | "day" | "light" | "dark" | "bench" | "bench-light";
export type EffectiveTheme = Exclude<Theme, "system">;
type Ctx = { theme: Theme; effectiveTheme: EffectiveTheme; setTheme: (theme: Theme) => void; toggle: () => void };

const ThemeContext = createContext<Ctx | null>(null);

const STORAGE_KEY = "klyx-theme";
const CHANGE_EVENT = "klyx:theme-change";
export const THEMES: ReadonlyArray<{ id: Theme; label: string }> = [
  { id: "system", label: "System" },
  { id: "night", label: "Night" },
  { id: "day", label: "Day" },
  { id: "light", label: "Light (classic)" },
  { id: "dark", label: "Dark (classic)" },
  { id: "bench", label: "Bench (retiring)" },
  { id: "bench-light", label: "Bench Light (retiring)" },
];

const ORDER: Theme[] = ["night", "day", "light", "dark", "bench", "bench-light"];

function isTheme(value: string | null): value is Theme {
  return value === "system" || value === "night" || value === "day" || value === "light" ||
    value === "dark" || value === "bench" || value === "bench-light";
}

// The six palettes removed on 2026-10-14; all were dark.
const RETIRED = new Set(["midnight", "graphite", "crimson", "forest", "amber", "violet"]);
const BENCH_MIGRATED_KEY = "klyx-theme-bench-migrated";
const LINE_MIGRATED_KEY = "klyx-theme-line-migrated";

// initial applies the one-time move to The Line (spec §2.1). A choice saved
// before it maps once onto night or day - bench onto night, bench-light onto
// day, a retired palette onto night, and a light or dark saved even before
// the July bench migration onto day or night. A choice made afterwards
// sticks, classic light and dark included; the flag is never read again.
export function initial(): Theme {
  const saved = localStorage.getItem(STORAGE_KEY);
  if (localStorage.getItem(LINE_MIGRATED_KEY)) return isTheme(saved) ? saved : "night";
  localStorage.setItem(LINE_MIGRATED_KEY, "1");
  const benchMigrated = !!localStorage.getItem(BENCH_MIGRATED_KEY);
  localStorage.setItem(BENCH_MIGRATED_KEY, "1");
  if (saved === null) return "night";
  if (saved === "bench" || RETIRED.has(saved)) return "night";
  if (saved === "bench-light") return "day";
  if (!benchMigrated && saved === "light") return "day";
  if (!benchMigrated && saved === "dark") return "night";
  return isTheme(saved) ? saved : "night";
}

const isLight = (t: EffectiveTheme) => t === "day" || t === "light" || t === "bench-light";

function prefersDark(): boolean {
  return typeof window !== "undefined" && typeof window.matchMedia === "function"
    ? window.matchMedia("(prefers-color-scheme: dark)").matches
    : false;
}

export function resolveTheme(theme: Theme): EffectiveTheme {
  // "system" follows the OS into night or day - The Line is the app's look;
  // the OS only chooses dark or light.
  return theme === "system" ? (prefersDark() ? "night" : "day") : theme;
}

function applyTheme(theme: Theme): EffectiveTheme {
  const effective = resolveTheme(theme);
  document.documentElement.setAttribute("data-theme-choice", theme);
  document.documentElement.setAttribute("data-theme", effective);
  document.documentElement.style.colorScheme = isLight(effective) ? "light" : "dark";
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
  const current: Theme = isTheme(stored) ? stored : "night";
  const next = ORDER[(Math.max(0, ORDER.indexOf(current)) + 1) % ORDER.length];
  setThemeChoice(next);
  return next;
}

export function ThemeProvider({ children }: { children: ReactNode }) {
  const [theme, setTheme] = useState<Theme>(initial);
  const [effectiveTheme, setEffectiveTheme] = useState<EffectiveTheme>(() => resolveTheme(theme));

  useEffect(() => {
    setEffectiveTheme(applyTheme(theme));
  }, [theme]);

  const toggle = () => setTheme((t) => ORDER[(Math.max(0, ORDER.indexOf(t)) + 1) % ORDER.length]);
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
