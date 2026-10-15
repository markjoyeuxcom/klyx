import type { LineDTO, LineStationDTO } from "../store/line";
import type { LineGitDTO, LineGitStationDTO } from "../store/lineGit";
import { short } from "./stationStrip";

// The line with Git history (The Line slice 6d, parent §3.4): words for an
// ordered station, T and its freshness, and why there is no ordering. Every
// word comes from the fenced LineGitDTO; nothing claims a delivery path.

const hhmm = (iso: string) => { const d = new Date(iso); const p = (n: number) => String(n).padStart(2, "0"); return `${p(d.getHours())}:${p(d.getMinutes())}`; };
const commits = (n: number) => `${n} commit${n === 1 ? "" : "s"}`;

export function gitStation(git: LineGitDTO | null, cluster: string): LineGitStationDTO | null {
  if (!git || git.state !== "ready") return null;
  return git.stations.find((s) => s.cluster === cluster) ?? null;
}

// orderedText: a cell's revision words, ordered against T.
export function orderedText(g: LineGitStationDTO, git: LineGitDTO): string {
  const t = git.target?.name ?? "the target";
  switch (g.state) {
    case "at": return `${short(g.sha)} · at ${t}`;
    case "behind": return g.held ? `${short(g.sha)} · held · ${g.n} behind` : `${short(g.sha)} · ${g.n} behind ${t}`;
    case "ahead": return `${short(g.sha)} · ${g.n} ahead of ${t}`;
    case "diverged": return `${short(g.sha)} · diverged at ${short(g.mergeBase ?? "")}`;
    case "unrelated": return `${short(g.sha)} · unrelated history`;
    default: return `${short(g.sha)} · not in fetched history`;
  }
}

// targetText names T and its freshness: "main at a1b9e3c · fetched 20:31",
// or the last good history beside a failed fetch.
export function targetText(git: LineGitDTO | null): string | null {
  const t = git?.state === "ready" ? git.target : null;
  if (!t || !t.sha) return null;
  const at = `${t.name} at ${short(t.sha)}`;
  if (!t.fetchedAt) return at;
  return t.error ? `${at} · history as of ${hhmm(t.fetchedAt)}; fetch failed: ${t.error}` : `${at} · fetched ${hhmm(t.fetchedAt)}`;
}

// historyNote says why the line is not ordered, when Git could order it.
export function historyNote(git: LineGitDTO | null): string | null {
  if (!git || git.state === "ready" || git.state === "none") return null;
  if (git.state === "unapproved" && git.approve) return `Git history: approve ${git.approve} in Settings > Git to order the line by its commits`;
  if (git.state === "checking") return "Git history: checking…";
  return `Git history: ${git.reason || git.state}`;
}

// heldText: "held, 3 commits behind main: attempted a1b9e3c, failing".
export function heldText(g: LineGitStationDTO, git: LineGitDTO): string | null {
  if (g.state !== "behind" || !g.held) return null;
  return `held, ${commits(g.n ?? 0)} behind ${git.target?.name ?? "the target"}: ${g.held.text}`;
}

// ordering counts the compared stations' ordered states, or null when any
// compared station is not ordered - then the headline stays on R.
export function ordering(dto: LineDTO, git: LineGitDTO | null): { at: number; behind: number; ahead: number; diverged: number; unrelated: number; compared: number } | null {
  if (!git || git.state !== "ready") return null;
  const compared = [...(dto.stations ?? []), ...(dto.siding ?? [])].filter((s: LineStationDTO) => ["matches", "differs", "mixed", "applied unknown"].includes(s.state));
  if (compared.length === 0) return null;
  const c = { at: 0, behind: 0, ahead: 0, diverged: 0, unrelated: 0, compared: compared.length };
  for (const s of compared) {
    const g = gitStation(git, s.cluster);
    if (!g || g.state === "unknown") return null;
    c[g.state as "at" | "behind" | "ahead" | "diverged" | "unrelated"]++;
  }
  return c;
}

// commitHref opens a commit through the repository's link template.
export function commitHref(git: LineGitDTO, sha: string): string | null {
  return git.commitLink ? git.commitLink.replace("{sha}", sha) : null;
}
