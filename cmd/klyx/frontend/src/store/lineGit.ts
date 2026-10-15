import { create } from "zustand";
import type { LineGitDTO } from "../../bindings/github.com/moomora/klyx/internal/appbridge/models.js";
import type { LineDTO } from "./line";

export type { LineGitDTO, LineGitStationDTO, LineGitCommitDTO, LineGitOnlyDTO } from "../../bindings/github.com/moomora/klyx/internal/appbridge/models.js";

// The line's Git ordering (The Line slice 6d): the backend's one LineGitDTO,
// pushed after every LineDTO, fetch and approval change. The store keeps a
// DTO only when its version is newer than the one it holds.
export const useLineGit = create<{ dto: LineGitDTO | null }>(() => ({ dto: null }));

export function applyLineGit(dto: LineGitDTO | null | undefined): boolean {
  if (!dto) return false;
  const held = useLineGit.getState().dto;
  if (held && dto.version <= held.version) return false;
  useLineGit.setState({ dto });
  return true;
}

export function resetLineGitStore(): void {
  useLineGit.setState({ dto: null });
}

// lineGitFor is the fence: an ordering is shown only beside the LineDTO
// version it answers, so it never sits on another line's facts.
export function lineGitFor(git: LineGitDTO | null, line: LineDTO | null): LineGitDTO | null {
  return git && line && git.lineVersion === line.version ? git : null;
}

// useLineGitFor is the fenced ordering for the line the caller shows.
export function useLineGitFor(line: LineDTO | null): LineGitDTO | null {
  return lineGitFor(useLineGit((s) => s.dto), line);
}
