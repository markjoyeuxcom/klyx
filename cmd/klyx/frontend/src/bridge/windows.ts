import { WindowsService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";

type ActionResultDTO = { ok: boolean; error: string };

// openLogsWindow asks the Go side to open a native window tailing one
// container's logs. Returns true on success. Errors (validation or bridge
// throw) resolve to false so callers can keep the dock open as a fallback.
export async function openLogsWindow(
  cluster: string,
  namespace: string,
  pod: string,
  container: string,
): Promise<boolean> {
  try {
    const r = (await WindowsService.OpenLogsWindow(cluster, namespace, pod, container)) as ActionResultDTO;
    return !!r?.ok;
  } catch {
    return false;
  }
}

// openLogHistoryWindow opens a native window on a pod's log history (coverage
// S8b), pinned to its UID. Same false-on-failure contract.
export async function openLogHistoryWindow(cluster: string, namespace: string, pod: string, uid: string, createdUnix?: number, gone?: boolean): Promise<boolean> {
  try {
    const r = (await WindowsService.OpenLogHistoryWindow(cluster, namespace, pod, uid, createdUnix ?? 0, !!gone)) as ActionResultDTO;
    return !!r?.ok;
  } catch {
    return false;
  }
}

// openWorkloadLogsWindow opens a native window tailing the AGGREGATE logs of a
// workload (all pods, fan-in with prefixes). Same false-on-failure contract.
export async function openWorkloadLogsWindow(
  cluster: string,
  namespace: string,
  kind: string,
  name: string,
  container: string,
): Promise<boolean> {
  try {
    const r = (await WindowsService.OpenWorkloadLogsWindow(cluster, namespace, kind, name, container)) as ActionResultDTO;
    return !!r?.ok;
  } catch {
    return false;
  }
}
