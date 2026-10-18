import type { KindCoverageDTO, RuleDTO } from "../store/fleet";
import { VeleroService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";

// Coverage S2a: the Velero lens's rows, mirroring internal/appbridge
// velero_dto.go. Velero's text never crosses: every message is Klyx's fixed
// class, and hooks, config values and Secrets are never carried.

export type VeleroRunDTO = {
  uid: string;
  name: string;
  phase: string;
  at?: string; // execution time; absent when unknown
  created?: string;
  started?: string;
  completed?: string;
  proven: boolean;
  timing: "local" | "disputed" | "synced";
  errors?: number;
  warnings?: number;
  class?: string;
};

export type VeleroTemplateDTO = {
  storageLocation?: string;
  storageLocationInvalid?: boolean;
  ttl?: string;
  snapshotVolumes?: boolean;
  defaultVolumesToFsBackup?: boolean;
  snapshotMoveData?: boolean;
  volumeSnapshotLocations: string[];
  includedNamespaces: string[];
  includedCount: number;
  excludedNamespaces: string[];
  excludedCount: number;
  hooks: number;
};

export type VeleroLocationLinkDTO = {
  state: "found" | "not found" | "not readable" | "incomplete" | "invalid" | "server default";
  name?: string;
  byDefault?: boolean;
  label?: string;
  role?: string;
  validated?: string;
};

export type VeleroScheduleDTO = {
  uid: string;
  gvr: string;
  namespace: string;
  name: string;
  generation: number;
  created?: string;
  cron: string;
  cronTooLong?: boolean;
  zone?: string;
  library?: string;
  paused: boolean;
  phase: string;
  useOwnerReferences: boolean;
  lastBackup?: string;
  lastSkipped?: string;
  due?: string;
  dueZero?: boolean;
  dueUnknown?: string;
  validation: string[];
  nName?: string;
  nState?: "found" | "unseen" | "not-retained" | "unidentified" | "gone" | "";
  n?: VeleroRunDTO;
  deciding?: VeleroRunDTO;
  previous?: VeleroRunDTO;
  previousNamed?: boolean;
  blocker?: VeleroRunDTO;
  fact?: VeleroRunDTO;
  runs: VeleroRunDTO[];
  newer: VeleroRunDTO[];
  synced: number;
  template: VeleroTemplateDTO;
  location: VeleroLocationLinkDTO;
  resumed?: boolean;
  horizon: VeleroHorizonDTO;
  reason?: string;
  rule: RuleDTO;
};

// Coverage S2b §4: the latest retained successful backup, a fact beside the
// verdict, never "restorable". text and partialText are Klyx's wording, the
// same on every surface: a cell may shorten it, never drop its confidence.
export type VeleroHorizonDTO = {
  state: "found" | "none" | "unreadable";
  success?: VeleroRunDTO;
  at?: string; // its completion, or its start when untimed
  proven: boolean;
  untimed?: boolean;
  orderUnknown?: boolean;
  incomplete?: boolean;
  window?: string;
  tier0?: boolean;
  partial?: VeleroRunDTO;
  partialProven?: boolean;
  partialOrderUnknown?: boolean;
  expired?: number;
  nextExpiry?: string;
  text: string;
  partialText?: string;
};

export type VeleroScheduleRefDTO = { namespace: string; name: string; label: string; role: string };

export type VeleroLocationDTO = {
  uid: string;
  gvr: string;
  namespace: string;
  name: string;
  created?: string;
  provider?: string;
  bucket?: string;
  prefix?: string;
  caCertSet?: boolean;
  caCertRef?: string;
  credential?: string;
  credentialKey?: string;
  default: boolean;
  accessMode?: string;
  syncPeriod?: string;
  validationFrequency?: string;
  phase: string;
  lastValidation?: string;
  lastSynced?: string;
  message?: string;
  messageWithheld?: boolean;
  configKeys: string[];
  configCount: number;
  region?: string;
  s3Url?: string;
  publicUrl?: string;
  schedules: VeleroScheduleRefDTO[];
  schedulesState: string;
  expired: number;
  expiredState: string;
  gcFailures?: Record<string, number>;
  severalDefaults?: boolean;
  reason?: string;
  rule: RuleDTO;
};

export type VeleroBackupDTO = {
  uid: string;
  gvr: string;
  namespace: string;
  name: string;
  created?: string;
  started?: string;
  completed?: string;
  expires?: string;
  phase: string;
  scheduleLabel?: string;
  storageLocation?: string;
  storageLocationInvalid?: boolean;
  errors: number;
  warnings: number;
  itemsTotal?: number;
  itemsBackedUp?: number;
  queuePosition?: number;
  failure?: string;
  validation: string[];
  validationCount: number;
  hooksAttempted?: number;
  hooksFailed?: number;
  operationsFailed?: number;
  membership: string;
  membershipSchedule?: string;
  disputed?: boolean;
  // S2b §3: counted rows are S2a's recent unscheduled ones and carry Klyx's
  // verdict in rule; a row not counted is a listed fact, with no rule.
  counted: boolean;
  schedule?: string;
  // unknown: no match in an incomplete or unreadable Schedules read, which
  // proves nothing; a match from one is kept, and attributionCoverage says so
  attribution?: "proven" | "named" | "synced" | "unknown" | "";
  attributionDisputed?: boolean;
  attributionCoverage?: "incomplete" | "unreadable" | "";
  expired?: boolean;
  gcFailure?: string;
  reason?: string;
  rule?: RuleDTO;
};

export type VeleroRestoreSourceDTO = {
  state: "found" | "not-found" | "not-retained" | "not-in-last-read" | "unreadable" | "none" | "invalid";
  text: string;
  backup?: VeleroRunDTO;
  schedule?: string;
  attribution?: string;
  attributionCoverage?: string;
  asOf?: string;
  class?: string;
};

// A Restore: a fact, with no verdict (S2b §2).
export type VeleroRestoreDTO = {
  uid: string;
  namespace: string;
  name: string;
  created?: string;
  backupName?: string;
  backupNameInvalid?: boolean;
  scheduleName?: string;
  scheduleNameInvalid?: boolean;
  includedNamespaces: string[];
  includedCount: number;
  excludedNamespaces: string[];
  excludedCount: number;
  mapping: { from: string; to: string }[];
  mappingCount: number;
  resourcesIncluded?: number;
  resourcesExcluded?: number;
  labelSelector?: boolean;
  orLabelSelectors?: number;
  restorePVs?: boolean;
  existingResourcePolicy?: string;
  itemOperationTimeout?: string;
  hooks: number;
  resourceModifier?: string;
  resourcePolicy?: string;
  phase: string;
  started?: string;
  completed?: string;
  errors: number;
  warnings: number;
  itemsTotal?: number;
  itemsRestored?: number;
  operationsAttempted?: number;
  operationsCompleted?: number;
  operationsFailed?: number;
  hooksAttempted?: number;
  hooksFailed?: number;
  failure?: string;
  failureWithheld?: boolean;
  validation: string[];
  validationCount: number;
  cut?: boolean;
  source: VeleroRestoreSourceDTO;
};

export type VeleroRestoresDTO = {
  cluster: string;
  readId: string;
  state: "read" | "not-served" | "forbidden" | "error";
  error?: string;
  restores: VeleroRestoreDTO[];
  cap: number;
  more?: boolean;
  remaining?: number; // the server's count, only when it supplied one
  readAt?: string;
  backups: string; // the Backups coverage the source join read
};

export type VeleroFleetHorizonDTO = {
  cluster: string;
  state: "read" | "no-observer" | "no-velero";
  asOf?: string;
  coverage?: string;
  kinds: KindCoverageDTO[];
  schedules: VeleroScheduleDTO[];
};

// One on-demand Restores read; the caller fences the reply by cluster and
// readId (S2b §3).
export async function getVeleroRestores(cluster: string, readId: string): Promise<VeleroRestoresDTO> {
  try {
    return (await VeleroService.GetVeleroRestores(cluster, readId)) as unknown as VeleroRestoresDTO;
  } catch (e) {
    return { cluster, readId, state: "error", error: e instanceof Error ? e.message : "the read failed", restores: [], cap: 0, backups: "" };
  }
}

// One cluster's schedules for the fleet Backups view.
export async function getBackupHorizon(cluster: string): Promise<VeleroFleetHorizonDTO> {
  return (await VeleroService.GetBackupHorizon(cluster)) as unknown as VeleroFleetHorizonDTO;
}
