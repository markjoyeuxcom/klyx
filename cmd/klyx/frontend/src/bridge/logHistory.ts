import { LogHistoryService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";

// Coverage S8b: pod log history from the cluster's Loki. The backend holds
// each view's page set (mode, branches, range and source locked, §4); the
// view says restart, or next after the page it last took (set, after), so a
// reply it discarded is read again, never skipped.

export type PodHistoryRequest = {
  cluster: string; view: string; restart: boolean; namespace: string; pod: string; uid: string;
  container?: string; createdUnix?: number; gone?: boolean; rangeSeconds?: number; mode?: "auto" | "name" | "uid"; skip?: number[];
  set?: number; after?: number;
};
export type HistoryLine = { ts: string; line: string; container?: string; branch: number; clipped?: boolean };
export type HistoryMarker = { branch: number; ts: string; count: number; refused?: string };
export type HistoryPage = {
  view: string; lines: HistoryLine[]; selectors: string[]; copyable: boolean; mode: "uid" | "name" | ""; uidOffer: boolean;
  strength: string; source: string; tenant?: string; sentence?: string; start: string; end: string; done: boolean; more?: boolean; markers?: HistoryMarker[];
  noStreams?: string; reset?: boolean; error?: string;
  set?: number; page?: number; // this page (absent: nothing to continue)
};

export async function readPodHistory(req: PodHistoryRequest): Promise<HistoryPage> {
  return (await LogHistoryService.ReadPodHistory(req as never)) as unknown as HistoryPage;
}

export function closeHistoryView(view: string): void {
  void LogHistoryService.CloseView(view);
}
