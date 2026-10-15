// MOD names the platform's command modifier in shortcut hints.
const isMac = typeof navigator !== "undefined" && /mac/i.test(navigator.platform ?? "");
export const MOD = isMac ? "⌘" : "Ctrl";
