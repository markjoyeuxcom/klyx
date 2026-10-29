import { EndpointsService, SourcesService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";
import type { EndpointAction } from "./endpoints";
import { askApply, askPlan, type ApplyResult, type Plan } from "./plan";

// Settings S3a §5-§8, S3b §5-§8: Observability's sources of every kind -
// the installations in force and each cluster's subscription - their plans
// and Apply, an installation's credential, Detect and the migration offer.

// A kind of source; each is its own panel's.
export type SourceKind = "alertmanager" | "loki" | "metrics";

export type ServiceRef = { namespace: string; name: string; port: string; scheme?: string; path?: string };

// An installation's fields, as edited. It carries no secret: an endpoint's
// credential is the keychain's.
export type SourceDraft = {
  id: string;
  name?: string;
  host?: string;
  serviceRef?: ServiceRef;
  discover?: boolean;
  endpoint?: string;
  tlsSkipVerify?: boolean;
  authType?: string;
  authUsername?: string;
  tenant?: string;
  memberLabel?: string;
  servesGroups?: string[];
  servesClusters?: string[];
};

// One installation in force: its fields, where it is, its credential's
// state, its subscribers, and its latest read.
export type Installation = SourceDraft & {
  revision: string;
  where: string;
  credential: string;
  subscribers: string[];
  readAt?: string;
  unmatched?: number;
  truncated?: boolean;
  readError?: string;
};

// A cluster's own subscription block: an installation with its scope,
// tenant and match, or off.
export type Subscription = { source?: string; scope?: string; off?: boolean; tenant?: string; match?: Record<string, string> };

// One running cluster's alerts: how they resolved, the scope sentence,
// what its own block shadows, and that block (or that it is a legacy one).
export type SubscriptionRow = {
  cluster: string;
  mode: string;
  via: string;
  source?: string;
  sentence: string;
  shadowed?: string[];
  block?: Subscription;
  legacy?: boolean;
  // a subscription's revisions in force, which its Test sends back
  instRevision?: string;
  subRevision?: string;
};

export type SourcesState = {
  kind: SourceKind;
  sentence: string;
  installations: Installation[];
  subscriptions: SubscriptionRow[];
  fileDiffers: boolean;
  fileError?: string;
};

export type InstallationEdit = { op: "add" | "edit" | "remove"; source: SourceDraft };
// A cluster's own block as it should be; null removes it.
export type SubscriptionEdit = { cluster: string; block: Subscription | null };
// A draft is of one kind: its installation's and its subscription blocks'.
export type SourcesDraft = { kind?: SourceKind; installation?: InstallationEdit; subscriptions?: SubscriptionEdit[] };

export type DetectScope = { kind?: SourceKind; group?: string; cluster?: string };
export type DetectCluster = { cluster: string; outcome: "found" | "none" | "ambiguous" | "unreadable"; detail?: string };
export type DetectProposal = {
  host: string;
  where: string;
  sampledAt?: string;
  sample: string;
  memberLabel?: string;
  candidates?: string[];
  membersSeen?: string[];
  notes: string[];
  draft: SourcesDraft;
};
export type DetectResult = { clusters: DetectCluster[]; proposals: DetectProposal[]; error?: string };

export type ScopeChange = { cluster: string; before: string; after: string; change: string };
export type MigrationOffer = {
  kind: SourceKind;
  where: string;
  clusters: string[];
  draft: SourcesDraft;
  scopes: ScopeChange[];
  conflicts: string[];
  // the installation's fields the conflicts leave to the person: auth,
  // tlsSkipVerify. The draft holds no value for them; the editor asks.
  unresolved: string[];
  choices: string[];
  credential?: string;
};

const why = (e: unknown) => (e instanceof Error ? e.message : String(e));

// getSources is a kind's state, null when the read failed: never an empty
// state.
export async function getSources(kind: SourceKind = "alertmanager"): Promise<SourcesState | null> {
  try {
    return (await SourcesService.GetSources(kind)) as SourcesState;
  } catch {
    return null;
  }
}

export const planSources = (d: SourcesDraft): Promise<Plan> => askPlan("observability", "save", () => SourcesService.PlanSources(d as never));
export const planSourcesFromFile = (kind: SourceKind = "alertmanager"): Promise<Plan> =>
  askPlan("observability", "file", () => SourcesService.PlanSourcesFromFile(kind));
export const applySources = (digest: string): Promise<ApplyResult> => askApply(() => SourcesService.ApplySources(digest));

export async function detect(scope: DetectScope): Promise<DetectResult> {
  try {
    return (await SourcesService.Detect(scope as never)) as DetectResult;
  } catch (e) {
    return { clusters: [], proposals: [], error: why(e) };
  }
}

export async function migrationOffers(kind: SourceKind = "alertmanager"): Promise<MigrationOffer[] | null> {
  try {
    return (await SourcesService.MigrationOffers(kind)) as MigrationOffer[];
  } catch {
    return null;
  }
}

const act = async (call: () => Promise<unknown>): Promise<EndpointAction> => {
  try {
    return (await call()) as EndpointAction;
  } catch (e) {
    return { ok: false, message: why(e) };
  }
};

export const setSourceCredential = (id: string, revision: string, secret: string) => act(() => EndpointsService.SetSourceCredential(id, revision, secret));
export const removeSourceCredential = (id: string, revision: string) => act(() => EndpointsService.RemoveSourceCredential(id, revision));
export const testSource = (id: string, revision: string) => act(() => EndpointsService.TestSource(id, revision));
// A member's Loki subscription Test: its name and the revisions its row
// showed, never a tenant or a match.
export const testSubscription = (cluster: string, kind: SourceKind, instRevision: string, subRevision: string) =>
  act(() => SourcesService.TestSubscription(cluster, kind, instRevision, subRevision));
