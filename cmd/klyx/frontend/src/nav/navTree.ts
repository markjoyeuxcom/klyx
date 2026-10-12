// Opening the navigation tree from elsewhere (the mast Attention button).
// The Attention button opens it on the first destination needing attention.
export const OPEN_TREE_EVENT = "klyx:open-nav-tree";
export const openNavTree = (focus: "attention" | "current" = "attention") =>
  window.dispatchEvent(new CustomEvent(OPEN_TREE_EVENT, { detail: { focus } }));
