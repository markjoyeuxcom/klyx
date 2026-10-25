import snapshot from "./registry.snapshot.json";

// The registry as the frontend reads it (Settings S1 §2): the golden
// snapshot internal/settings writes, so the frontend never renders an id
// the Go registry lacks. SettingsService.Registry() serves the same data.

export const SETTINGS_SECTIONS = ["fleet", "observability", "ai", "git", "notifications", "appearance", "data", "diagnostics"] as const;
export type SettingsSection = (typeof SETTINGS_SECTIONS)[number];

export const SECTION_TITLES: Record<SettingsSection, string> = {
  fleet: "Fleet",
  observability: "Observability",
  ai: "AI",
  git: "Git",
  notifications: "Notifications",
  appearance: "Appearance",
  data: "Data and storage",
  diagnostics: "Diagnostics",
};

export function isSettingsSection(v: unknown): v is SettingsSection {
  return typeof v === "string" && (SETTINGS_SECTIONS as readonly string[]).includes(v);
}

export type Disposition = "editable" | "effective-file-only" | "effective" | "deferred" | "fixed";

export type SettingEntry = {
  id: string;
  section: SettingsSection;
  label: string;
  effect: string;
  scope: string;
  default: string;
  validation: string;
  takesEffect: string;
  test?: string;
  save: string;
  disposition: Disposition;
  source: "schema" | "env" | "store" | "bound" | "credential";
};

export const ENTRIES: readonly SettingEntry[] = (snapshot as { entries: SettingEntry[] }).entries;
const BY_ID = new Map(ENTRIES.map((e) => [e.id, e]));

export function settingEntry(id: string): SettingEntry | undefined {
  return BY_ID.get(id);
}

export function isRegistered(id: string): boolean {
  return BY_ID.has(id);
}

// An anchor is a registry id, plus an instance for a repeated row
// ("clusters[].protected@aks-we-prd").
export function parseAnchor(anchor: string): { id: string; instance?: string } {
  const at = anchor.indexOf("@");
  return at < 0 ? { id: anchor } : { id: anchor.slice(0, at), instance: anchor.slice(at + 1) };
}

export function anchorOf(id: string, instance?: string): string {
  return instance === undefined ? id : `${id}@${instance}`;
}

// A control's DOM id, from its registry id and instance key: the anchor's
// UTF-8 bytes in hex, so every pair has its own id and no instance can
// collide with another's, or with a suffix ("-row", "-effect") another
// adds (Codex's S1 implementation review, R5: % replaced by _ was not).
const utf8 = new TextEncoder();
export function settingDomId(id: string, instance?: string): string {
  return `setting-${Array.from(utf8.encode(anchorOf(id, instance)), (b) => b.toString(16).padStart(2, "0")).join("")}`;
}

export function repeated(id: string): boolean {
  return id.includes("[]");
}

// Search (§3): by label and by effect, case-insensitively and by word: every
// word of the query must start a word of the label or the effect.
export function searchSettings(query: string): SettingEntry[] {
  const words = query.toLowerCase().split(/[^a-z0-9]+/).filter(Boolean);
  if (words.length === 0) return [];
  return ENTRIES.filter((e) => {
    const hay = `${e.label} ${e.effect}`.toLowerCase().split(/[^a-z0-9]+/).filter(Boolean);
    return words.every((w) => hay.some((h) => h.startsWith(w)));
  });
}
