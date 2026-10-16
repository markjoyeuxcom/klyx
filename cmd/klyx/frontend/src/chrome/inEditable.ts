// inEditable: the key's target is a field being edited, which keeps its
// own keys. A leaf module (no store, no bindings), so layout code can use it.
export function inEditable(t: EventTarget | null): boolean {
  const el = t as HTMLElement | null;
  if (!el) return false;
  const tag = el.tagName;
  if (tag === "INPUT" || tag === "TEXTAREA") return true;
  // isContentEditable is the standard property but jsdom may not implement it;
  // fall back to checking the contentEditable attribute string.
  if (el.isContentEditable === true) return true;
  if ((el as HTMLElement).contentEditable === "true") return true;
  return false;
}
