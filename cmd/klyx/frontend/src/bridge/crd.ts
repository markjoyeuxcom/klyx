import { useFleet, CRDGroupDTO, CRDCountDTO, crdCountKey, ResourceRef, InstanceDTO, InstanceRef, InstanceDetailDTO, GitOpsOwnershipDTO, AdhocResultDTO } from "../store/fleet";
import { CRDService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";
import { Clipboard } from "@wailsio/runtime";

export async function listCRDs(cluster: string): Promise<void> {
  useFleet.getState().setCRDLoading(cluster);
  try {
    const groups = (await CRDService.ListCRDs(cluster)) as CRDGroupDTO[];
    useFleet.getState().setCRDs(cluster, groups ?? []);
  } catch (e) {
    // Clear the loading flag so the view doesn't get stuck on "Loading…".
    if (useFleet.getState().crd.cluster === cluster) {
      useFleet.setState((s) => ({ crd: { ...s.crd, loading: false } }));
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
  const key = crdCountKey(group, version, plural);
  if (inFlight.has(key)) return;
  inFlight.add(key);
  await acquireCountSlot();
  try {
    const c = (await CRDService.CountKind(cluster, group, version, plural)) as CRDCountDTO;
    useFleet.getState().setCRDCount(key, c);
  } finally {
    inFlight.delete(key);
    releaseCountSlot();
  }
}

type InstancePageDTO = { items: InstanceDTO[]; nextToken: string };
export type ActionResult = { ok: boolean; error: string };

export async function listInstancePage(cluster: string, ref: ResourceRef, token = ""): Promise<InstancePageDTO> {
  const page = (await CRDService.ListInstances(cluster, ref.group, ref.version, ref.plural, token)) as InstancePageDTO;
  return { items: page.items ?? [], nextToken: page.nextToken ?? "" };
}

export async function loadInstances(cluster: string, ref: ResourceRef, token?: string): Promise<void> {
  if (!token) useFleet.getState().setInstancesLoading(ref);
  try {
    const page = await listInstancePage(cluster, ref, token ?? "");
    // Drop a stale page if the user navigated to a different kind meanwhile.
    const cur = useFleet.getState().instances.ref;
    if (!cur || cur.group !== ref.group || cur.plural !== ref.plural) return;
    // First page REPLACES (idempotent against StrictMode's double effect invoke and
    // any re-trigger); only "load more" (token set) appends.
    if (token) useFleet.getState().addInstancePage(page.items ?? [], page.nextToken ?? "");
    else useFleet.getState().setInstancePage(page.items ?? [], page.nextToken ?? "");
  } catch (e) {
    console.error("loadInstances", e);
    // Clear the loading flag so the view doesn't get stuck on "Loading…".
    const cur = useFleet.getState().instances.ref;
    if (cur && cur.group === ref.group && cur.plural === ref.plural) {
      useFleet.setState((s) => ({ instances: { ...s.instances, loading: false } }));
    }
  }
}

export async function getInstanceDetail(cluster: string, resource: ResourceRef, instance: InstanceRef): Promise<void> {
  useFleet.getState().setInstanceDetailLoading(instance);
  try {
    const d = (await CRDService.GetInstanceDetail(cluster, resource.group, resource.version, resource.plural, instance.namespace, instance.name)) as InstanceDetailDTO;
    // Drop a stale detail if the user navigated to a different instance meanwhile.
    const cur = useFleet.getState().instanceDetail.ref;
    if (!cur || cur.namespace !== instance.namespace || cur.name !== instance.name) return;
    useFleet.getState().setInstanceDetail(d);
  } catch (e) {
    console.error("getInstanceDetail", e);
    // Clear the loading flag so the panel doesn't get stuck on "Loading…".
    const cur = useFleet.getState().instanceDetail.ref;
    if (cur && cur.namespace === instance.namespace && cur.name === instance.name) {
      useFleet.setState((s) => ({ instanceDetail: { ...s.instanceDetail, loading: false } }));
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
