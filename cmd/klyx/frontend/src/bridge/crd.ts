import { useFleet, sameResource, CRDGroupDTO, CRDCountDTO, crdCountKey, ResourceRef, InstanceDTO, InstanceRef, InstanceDetailDTO, GitOpsOwnershipDTO, AdhocResultDTO, AdhocObjectDTO, AdhocInventoryDTO , InstanceColumnDTO } from "../store/fleet";
import { CRDService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";
import { Clipboard } from "@wailsio/runtime";

export async function listCRDs(cluster: string): Promise<void> {
  useFleet.getState().setCRDLoading(cluster);
  try {
    // A failed read comes back WITH error (empty groups are then unreadable,
    // not "no CRDs installed").
    const r = (await CRDService.ListCRDs(cluster)) as { groups: CRDGroupDTO[]; error?: string };
    if (useFleet.getState().crd.cluster !== cluster) return; // stale: the browser moved on
    useFleet.getState().setCRDs(cluster, r?.groups ?? [], r?.error);
  } catch (e) {
    // Clear the loading flag AND record why: a rejected read must not render as
    // "no custom resources on this cluster" (Codex batch-2 review R7).
    if (useFleet.getState().crd.cluster === cluster) {
      const error = e instanceof Error ? e.message : String(e);
      useFleet.setState((s) => ({ crd: { ...s.crd, loading: false, error } }));
    }
    throw e; // callers .catch and log
  }
}

// inFlight dedupes concurrent count requests for the same kind: re-renders can
// re-trigger a count before the first resolves, and we must not fan out
// redundant metadata lists (the whole point of the lazy/capped strategy on
// high-cardinality kinds like Cilium's).
const inFlight = new Set<string>();

// The operator/scope/alphabetical group-by modes make every kind visible at
// once, which would otherwise dispatch one count per kind (~100+) simultaneously
// and slam the apiserver / client rate limiter. Cap how many counts run at a
// time; the rest queue and drain as slots free.
const COUNT_CONCURRENCY = 8;
let countActive = 0;
const countQueue: (() => void)[] = [];

function acquireCountSlot(): Promise<void> {
  if (countActive < COUNT_CONCURRENCY) {
    countActive++;
    return Promise.resolve();
  }
  return new Promise((resolve) => countQueue.push(resolve));
}

function releaseCountSlot(): void {
  const next = countQueue.shift();
  if (next) next(); // hand the freed slot straight to a waiter (countActive unchanged)
  else countActive--;
}

export async function countKind(cluster: string, group: string, version: string, plural: string): Promise<void> {
  const key = crdCountKey(cluster, group, version, plural);
  if (inFlight.has(key)) return;
  inFlight.add(key);
  await acquireCountSlot();
  try {
    const c = (await CRDService.CountKind(cluster, group, version, plural)) as CRDCountDTO;
    useFleet.getState().setCRDCount(key, c);
  } catch (e) {
    // A rejected count is unreadable ("?"), not "still counting" forever.
    useFleet.getState().setCRDCount(key, { count: 0, capped: false, error: e instanceof Error ? e.message : String(e) });
  } finally {
    inFlight.delete(key);
    releaseCountSlot();
  }
}

type InstancePageDTO = { items: InstanceDTO[]; nextToken: string; error?: string; notServed?: boolean; columns?: InstanceColumnDTO[] | null };
export type ActionResult = { ok: boolean; error: string };

// listInstancePage REJECTS on a failed read: the backend returns an empty page
// WITH error, and every caller (fleet-scope list, Overview risks, the list
// view) must treat that as unreadable - never as "no instances" (review
// 2026-10-03 #10). Throwing routes it through their existing error paths.
export async function listInstancePage(cluster: string, ref: ResourceRef, token = ""): Promise<InstancePageDTO> {
  const page = (await CRDService.ListInstances(cluster, ref.group, ref.version, ref.plural, token)) as InstancePageDTO;
  if (page?.error) throw new Error(page.error);
  // notServed (the kind is not installed) is a definitive empty page, not an error.
  return { items: page.items ?? [], nextToken: page.nextToken ?? "", notServed: !!page.notServed, columns: page.columns ?? null };
}

// listGen numbers instance-list reads: every first-page read (open, Refresh)
// starts a new list, and "load more" continues the list current when it was
// asked for (review 2026-10-13 #3: an old continuation appended obsolete rows
// to a refreshed list).
let listGen = 0;

export async function loadInstances(cluster: string, ref: ResourceRef, token?: string): Promise<void> {
  const gen = token ? listGen : ++listGen;
  if (!token) useFleet.getState().setInstancesLoading(cluster, ref);
  // A page belongs to exactly one cluster + GVR and one list read: drop it if
  // the user moved to another kind or cluster (review 2026-10-03 #5), or the
  // list was read again since.
  const current = () => {
    const s = useFleet.getState().instances;
    return gen === listGen && s.cluster === cluster && sameResource(s.ref, ref);
  };
  try {
    const page = await listInstancePage(cluster, ref, token ?? "");
    if (!current()) return;
    // First page REPLACES (idempotent against StrictMode's double effect invoke and
    // any re-trigger); only "load more" (token set) appends, and only onto the
    // list that still ends at the token it continued from.
    if (token) {
      if (useFleet.getState().instances.nextToken !== token) return;
      useFleet.getState().addInstancePage(page.items ?? [], page.nextToken ?? "", page.columns ?? null);
    } else useFleet.getState().setInstancePage(page.items ?? [], page.nextToken ?? "", page.columns ?? null);
  } catch (e) {
    console.error("loadInstances", e);
    // Clear the loading flag so the view doesn't get stuck on "Loading…", and
    // record WHY: the list must say it could not read, not that it is empty.
    // A failed continuation is fenced like a successful one: once the list no
    // longer ends at its token (another page landed), its error is obsolete.
    if (current() && (!token || useFleet.getState().instances.nextToken === token)) {
      const error = e instanceof Error ? e.message : String(e);
      useFleet.setState((s) => ({ instances: { ...s.instances, loading: false, error } }));
    }
  }
}

// detailGen numbers detail reads: only the latest read for the panel may
// land (review 2026-10-13 #3: an older read of the same object overwrote a
// newer one).
let detailGen = 0;

export async function getInstanceDetail(cluster: string, resource: ResourceRef, instance: InstanceRef): Promise<void> {
  const gen = ++detailGen;
  useFleet.getState().setInstanceDetailLoading(cluster, resource, instance);
  // A detail belongs to exactly one cluster + GVR + ns/name. DEV/STG/PRD share
  // namespace/name, so ns/name alone let a late DEV detail render under the PRD
  // header (review 2026-10-03 #5).
  const current = () => {
    const s = useFleet.getState().instanceDetail;
    return gen === detailGen && s.cluster === cluster && sameResource(s.resource, resource)
      && s.ref?.namespace === instance.namespace && s.ref?.name === instance.name;
  };
  try {
    const d = (await CRDService.GetInstanceDetail(cluster, resource.group, resource.version, resource.plural, instance.namespace, instance.name)) as InstanceDetailDTO;
    if (!current()) return;
    useFleet.getState().setInstanceDetail(d);
  } catch (e) {
    console.error("getInstanceDetail", e);
    // A failed read is the read's outcome, never the last good detail: the
    // panel says it could not load, and an object opened by UID reads "not
    // verified" (review of ff41862).
    if (current()) {
      useFleet.getState().setInstanceDetail({ kind: resource.kind, namespace: instance.namespace, name: instance.name, created: "", labels: {}, conditions: [], events: [], yaml: "",
        error: e instanceof Error ? e.message : String(e) });
    }
  }
}

export async function dryRunInstanceYAML(cluster: string, resource: ResourceRef, instance: InstanceRef, yaml: string): Promise<ActionResult> {
  const r = (await CRDService.ApplyResourceYAML(
    cluster,
    resource.group,
    resource.version,
    resource.plural,
    resource.kind,
    resource.scope,
    instance.namespace,
    instance.name,
    yaml,
    true,
  )) as ActionResult;
  return { ok: !!r.ok, error: r.error ?? "" };
}

export async function applyInstanceYAML(cluster: string, resource: ResourceRef, instance: InstanceRef, yaml: string): Promise<ActionResult> {
  const r = (await CRDService.ApplyResourceYAML(
    cluster,
    resource.group,
    resource.version,
    resource.plural,
    resource.kind,
    resource.scope,
    instance.namespace,
    instance.name,
    yaml,
    false,
  )) as ActionResult;
  return { ok: !!r.ok, error: r.error ?? "" };
}

// gitOpsOwnership fetches the authoritative pre-write verdict for a target
// object (who owns it / who reaches its namespace, with prune state). Null on
// failure - callers keep their label-based fallback warning.
export async function gitOpsOwnership(cluster: string, resource: ResourceRef, instance: InstanceRef): Promise<GitOpsOwnershipDTO | null> {
  try {
    return (await CRDService.GitOpsOwnership(cluster, resource.group, resource.kind, instance.namespace, instance.name)) as GitOpsOwnershipDTO;
  } catch {
    return null;
  }
}

// createResourceYAML creates the documents as NEW ad-hoc (non-GitOps, stamped)
// objects. Per-doc honest results; dryRun validates + carries prune warnings.
export async function createResourceYAML(cluster: string, yaml: string, dryRun: boolean): Promise<AdhocResultDTO[]> {
  return ((await CRDService.CreateResourceYAML(cluster, yaml, dryRun)) as AdhocResultDTO[]) ?? [];
}

// listAdhocObjects returns the Klyx-created scratch objects (the cleanup
// inventory), the kinds that could not be read, and an error when the sweep
// failed - a failed read is never an empty inventory.
export async function listAdhocObjects(cluster: string): Promise<AdhocInventoryDTO> {
  try {
    const r = (await CRDService.ListAdhocObjects(cluster)) as AdhocInventoryDTO | null;
    return { objects: r?.objects ?? [], unread: r?.unread ?? [], error: r?.error || undefined };
  } catch (e) {
    return { objects: [], unread: [], error: String(e) };
  }
}

// deleteAdhocObject deletes one Klyx-created scratch object (label-guarded and
// UID-pinned in the backend: never deletes anything Klyx did not create, nor a
// replacement under the same name).
export async function deleteAdhocObject(cluster: string, o: AdhocObjectDTO): Promise<{ ok: boolean; error: string }> {
  const r = (await CRDService.DeleteAdhocObject(cluster, o.group, o.version, o.plural, o.namespace, o.name, o.uid)) as { ok: boolean; error: string };
  return { ok: !!r.ok, error: r.error ?? "" };
}

export async function copyText(text: string): Promise<void> {
  await Clipboard.SetText(text);
}

type RevealResultDTO = { value: string; error: string };

// revealSecretKey calls the Go bridge to decode one Secret key value.
// Returns the decoded string on success, or null on error (caller handles
// user-visible error display). The value never touches the store.
export async function revealSecretKey(cluster: string, ns: string, name: string, key: string): Promise<string | null> {
  const r = (await CRDService.RevealSecretKey(cluster, ns, name, key)) as RevealResultDTO;
  if (r.error) return null;
  return r.value;
}
