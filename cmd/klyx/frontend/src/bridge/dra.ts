import type { RuleDTO } from "../store/fleet";

// Coverage S11b: the DRA lens's rows, mirroring internal/appbridge
// dra_rows.go. Never a CEL selector, a config, device data or an
// attribute; a driver's or the scheduler's message is shown locally only.

export type DRAReadingDTO = { kind: string; label: string; reason?: string; message?: string; native?: string; note?: string; rule?: string; reclassifyAtUnix?: number };

export type DRARequestDTO = { name: string; class?: string; mode?: string; count?: number; adminAccess?: boolean; subclasses?: string[] };
export type DRAResultDTO = { request: string; driver: string; pool: string; device: string; adminAccess?: boolean; shareId?: string };
export type DRAConsumerDTO = { resource: string; name: string; uid: string };
export type DRACondDTO = { type: string; status: string; reason?: string; message?: string };
export type DRADeviceStatusDTO = { driver: string; pool: string; device: string; shareId?: string; conditions: DRACondDTO[] };
export type DRAConsumerReadingDTO = { pod: string; row: string; kind: string; label: string; reason?: string; meaning?: string; message?: string };

export type DRAClaimDTO = {
  uid: string; namespace: string; name: string;
  rule: RuleDTO; reading: DRAReadingDTO;
  deleting?: boolean; finalizers?: string[]; ownerKind?: string; ownerName?: string; ownerUid?: string; podClaimName?: string; extended?: boolean;
  requests: DRARequestDTO[]; allocated: boolean; results: DRAResultDTO[];
  reservedCount: number; reserved: DRAConsumerDTO[]; devices: DRADeviceStatusDTO[];
  consumers: DRAConsumerReadingDTO[]; consumersOmitted?: number;
};

export type DRAVariantDTO = { minor: number; partitionable: boolean; outcome: string; devices: number; have?: number; want?: number; rule?: string };
export type DRAViewDTO = { node: string; agreed: boolean; kind?: string; variants: DRAVariantDTO[] };

export type DRAPoolDTO = {
  key: string; driver: string; pool: string; generation: number; slices: number;
  rule: RuleDTO; reading: DRAReadingDTO; views: DRAViewDTO[]; obsolete?: string[]; assumed: string; unread?: boolean;
  // nodes: how many nodes the slices name; the views past the row's bound
  // (the worst are kept) and obsolete nodes past it are counted
  nodes?: number; viewsOmitted?: number; obsoleteOmitted?: number;
  // devices by taint effect and slices by access form, at the generation
  taints?: DRACountDTO[]; access?: DRACountDTO[];
};

export type DRAClassDTO = { uid: string; name: string; selectors: number; configDrivers: string[]; extendedResourceName?: string };

// PodClaimRefDTO: one claim a pod references; state "resolved",
// "none-needed" (upstream skips it on purpose) or "not-created".
export type PodClaimRefDTO = { podClaim: string; source: string; claim?: string; state: string };
export type DeviceHealthEntryDTO = { id: string; health: string; message?: string };
export type DRACountDTO = { of: string; n: number };
// the counts are over every device the entry reports; devices keeps the
// unhealthy first, bounded
export type DeviceHealthDTO = { container: string; init?: boolean; name: string; devices: DeviceHealthEntryDTO[];
  unhealthy?: number; unknown?: number; healthy?: number; devicesOmitted?: number };
