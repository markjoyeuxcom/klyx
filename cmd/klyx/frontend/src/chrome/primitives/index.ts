// The Klyx primitive set: one source for the small components and style tokens
// every view shares. Import from here, not from per-view local copies.
export { StatusDot } from "./StatusDot";
export { Aspect, aspectOfVerdict, type AspectKind } from "./Aspect";
export {
  type Tone,
  toneColor,
  toneBg,
  toneBorder,
  readyColor,
  ellipsis,
  mono,
  well,
  btn,
  actionBtn,
} from "./styles";

// Re-export the already-shared chrome primitives so callers have one import site.
export { EmptyState } from "../EmptyState";
export { LiveIndicator } from "../LiveIndicator";
export { SkeletonRows } from "../SkeletonRows";
