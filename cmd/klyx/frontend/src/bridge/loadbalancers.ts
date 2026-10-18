import type { RuleDTO } from "../store/fleet";

// Coverage S4: the Load balancers lens's rows, mirroring internal/appbridge
// lb_dto.go. Counts that can exceed a JavaScript number (Cilium's IPv6
// pools) arrive as exact decimal strings; a missing MetalLB count is "not
// reported", never zero.

export type LBCondDTO = { type: string; status: string; reason?: string; message?: string; observedGeneration?: number; at?: string; current: boolean };
export type LBCandidateDTO = { controller: string; detected: boolean; limit?: string };
export type LBExpectedDTO = { class?: string; known: boolean; candidates: LBCandidateDTO[]; note?: string };
export type LBEventDTO = { reason: string; message?: string; count?: number; lastSeen?: number };
export type LBSvcLBDTO = { daemonSet?: string; pods: number; ready: number; pending: number; hostPortConflict?: boolean; pendingReason?: string; candidates?: number };
export type LBPoolRefDTO = { name: string; state: "found" | "missing" | "unverified"; namespace?: string };

export type LBDiagnosisDTO = {
  events: LBEventDTO[];
  eventCount: number;
  eventsRead: "complete" | "partial" | "unverified";
  svclb?: LBSvcLBDTO;
  svclbRead: boolean;
  cilium?: LBCondDTO;
  request: string[];
  pool?: LBPoolRefDTO;
  silent?: string;
  summary: string;
};

export type LBServiceDTO = {
  uid: string;
  gvr: string;
  namespace: string;
  name: string;
  generation: number;
  created?: string;
  class?: string;
  externalTrafficPolicy?: string;
  ipFamilies: string[];
  ipFamilyPolicy?: string;
  allocated: boolean;
  ips: string[];
  hostnames: string[];
  addressCount: number;
  entries: number;
  families: string[];
  portError?: boolean;
  portsErrorCond?: boolean;
  missingFamily?: string;
  oneFamilyPreferred?: boolean;
  expected: LBExpectedDTO;
  // set while pending; pendingObserved false means a lower bound
  pendingSince?: string;
  pendingObserved?: boolean;
  diagnosis?: LBDiagnosisDTO;
  cilium?: LBCondDTO;
  conditions: LBCondDTO[];
  conditionCount: number;
  limits: string[];
  reason?: string;
  rule: RuleDTO;
};

export type LBCountsDTO = { total?: string; totalState?: string; available?: string; availableState?: string; used?: string; usedState?: string };
export type LBMetalLBStatusDTO = { assignedIPv4?: number; availableIPv4?: number; assignedIPv6?: number; availableIPv6?: number };

export type LBPoolDTO = {
  kind: "cilium" | "metallb";
  uid: string;
  gvr: string;
  namespace?: string;
  name: string;
  generation: number;
  created?: string;
  blocks: string[];
  blockCount: number;
  disabled?: boolean;
  serviceSelector?: boolean;
  counts?: LBCountsDTO;
  conflict?: LBCondDTO;
  conditions: LBCondDTO[];
  labels?: Record<string, string>;
  labelsCut?: boolean;
  autoAssign?: boolean;
  serviceAllocation?: boolean;
  status?: LBMetalLBStatusDTO;
  selectedBy: string[];
  unresolved: string[];
  adsRead?: string;
  statesRead?: string;
  instance?: "state" | "workload" | "";
  invalid: string[];
  limits: string[];
  reason?: string;
  rule: RuleDTO;
};

export type LBStateDTO = {
  uid: string;
  gvr: string;
  namespace: string;
  name: string;
  role?: "controller" | "speaker";
  node?: string;
  result?: string;
  error?: string;
  created?: string;
  conditions: LBCondDTO[];
  conditionCount: number;
  reason?: string;
  rule: RuleDTO;
};
