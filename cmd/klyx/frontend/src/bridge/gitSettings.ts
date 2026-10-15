import { GitService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";

// Settings > Git (The Line slice 6c, slice 6 note §5). Klyx runs the system
// git only from fleet.yaml's approved git.repos URLs, exactly as written; a
// URL read from a cluster is only matched to one and shown scrubbed.

export type ActionResult = { ok: boolean; error: string };

export type GitSource = { cluster: string; origin: string; namespace: string; name: string; url: string };

export type GitRepo = {
  url: string;
  web?: string;
  key: string;
  matches: string[];
  sources: GitSource[];
  tracked: string[];
  trackNotes: string[];
  size: number;
  fetchedAt?: string;
  attemptedAt?: string;
  error?: string;
  needsRepair: boolean;
  fetching: boolean;
};

export type GitUnapproved = { key: string; serves: string; sources: GitSource[]; suggest?: string; refused?: string };

export type GitSettings = {
  path: string;
  binary: { ready: boolean; path?: string; version?: string; off?: string };
  helpers: string[];
  refusedHelpers: number;
  repos: GitRepo[];
  unapproved: GitUnapproved[];
  notes: string[];
  configError?: string;
};

function failed(e: unknown): ActionResult {
  return { ok: false, error: e instanceof Error ? e.message : String(e) };
}

export async function getGitSettings(): Promise<GitSettings | null> {
  try {
    return (await GitService.GetGitSettings()) as GitSettings;
  } catch (e) {
    console.error("git settings", e);
    return null;
  }
}

async function act(call: () => Promise<unknown>): Promise<ActionResult> {
  try {
    return (await call()) as ActionResult;
  } catch (e) {
    return failed(e);
  }
}

export const approveGitRepo = (url: string, web: string, serves: string) => act(() => GitService.ApproveGitRepo(url, web, serves));
export const removeGitRepo = (url: string) => act(() => GitService.RemoveGitRepo(url));
export const setGitRepoMatch = (url: string, key: string, on: boolean) => act(() => GitService.SetGitRepoMatch(url, key, on));
export const fetchGitRepo = (url: string) => act(() => GitService.FetchGitRepo(url));
export const clearGitCache = (url: string) => act(() => GitService.ClearGitCache(url));
