import type { RuleDTO } from "../store/fleet";

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
  reason?: string;
  rule: RuleDTO;
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
  reason?: string;
  rule: RuleDTO;
};
