import { STORAGE_KEYS, type StorageKey } from "./storageKeys";

// The only module that touches webview storage (Settings S1 §3.2). A key
// must be one storageKeys.ts declares: the type says so at compile time, and
// a membership check refuses a cast ("x" as StorageKey) before any access.
// Storage can be unavailable (privacy mode, a sandbox) or full, so every
// access is best-effort, as it always was: a read gives null, a write is
// dropped, and nothing throws.

export function isStorageKey(key: string): key is StorageKey {
  return Object.prototype.hasOwnProperty.call(STORAGE_KEYS, key);
}

function storageFor(key: StorageKey): Storage | null {
  if (!isStorageKey(key)) return null;
  try {
    return STORAGE_KEYS[key] === "session" ? globalThis.sessionStorage : globalThis.localStorage;
  } catch {
    return null;
  }
}

export function readStored(key: StorageKey): string | null {
  const s = storageFor(key);
  if (!s) return null;
  try {
    return s.getItem(key);
  } catch {
    return null;
  }
}

export function writeStored(key: StorageKey, value: string): void {
  const s = storageFor(key);
  if (!s) return;
  try {
    s.setItem(key, value);
  } catch {
    // best-effort: a full or unavailable storage keeps the choice for this session
  }
}
