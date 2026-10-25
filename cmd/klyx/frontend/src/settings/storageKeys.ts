// The webview storage keys Klyx keeps (Settings S1 §3.2), each with its
// tier. Every key has a registry entry ("store." + key); a test compares
// this table with the registry snapshot, each way. A new key is a new line
// here and a new registry entry.
export const STORAGE_KEYS = {
  "klyx-theme": "local",
  "klyx-theme-bench-migrated": "local",
  "klyx-theme-line-migrated": "local",
  "klyx.layout.v1.zoom": "local",
  "klyx.layout.v1.inspectorWidth": "local",
  "klyx.layout.v1.lensIndex": "local",
  "klyx-logs-dock-height": "local",
  "klyx.yaml.wrap": "local",
  "klyx-route": "local",
  "klyx-view-prefs": "local",
  "klyx-view-search": "session",
  "klyx-palette-frecency": "local",
  "klyx-action-log": "local",
} as const;

export type StorageKey = keyof typeof STORAGE_KEYS;
