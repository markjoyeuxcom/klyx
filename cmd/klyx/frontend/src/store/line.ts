import { create } from "zustand";
import type { LineDTO } from "../../bindings/github.com/moomora/klyx/internal/appbridge/models.js";

export type { LineDTO, LineStationDTO } from "../../bindings/github.com/moomora/klyx/internal/appbridge/models.js";

// The line (The Line slice 4 §5.1): the backend's one LineDTO, pushed on
// every change and read once at start. Never polled (parent §3.6). The
// store keeps a DTO only when its version is newer than the one it holds,
// whatever order the reply and the events arrive in.
export const useLine = create<{ dto: LineDTO | null }>(() => ({ dto: null }));

// applyLine offers a DTO; an equal or older version is ignored.
export function applyLine(dto: LineDTO | null | undefined): boolean {
  if (!dto) return false;
  const held = useLine.getState().dto;
  if (held && dto.version <= held.version) return false;
  useLine.setState({ dto });
  return true;
}

export function resetLineStore(): void {
  useLine.setState({ dto: null });
}
