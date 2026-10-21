import { useFleet } from "../store/fleet";
import type { ApplyConflictDTO, ConflictOwnerDTO } from "../bridge/crd";
import { wellKnownResourceRef } from "./resourceRefs";

// v0.15.1: a refused apply says each field's manager (the backend's text),
// and when a Kubernetes controller owns a field, offers the owner the edit
// belongs in: a Pod's ReplicaSet's Deployment, a Job's CronJob.

const WORKLOAD_KINDS = new Set(["Deployment", "StatefulSet", "DaemonSet"]);

function ownerOpener(cluster: string, o: ConflictOwnerDTO): (() => void) | null {
  if (o.group === "apps" && WORKLOAD_KINDS.has(o.kind)) {
    return () => {
      const nav = useFleet.getState();
      nav.setSection("workloads");
      nav.requestNavIntent({ kind: "select-workload", cluster, workloadKind: o.kind, namespace: o.namespace, name: o.name });
    };
  }
  const ref = wellKnownResourceRef(o.group, o.kind);
  if (!ref) return null;
  return () => {
    const nav = useFleet.getState();
    nav.openResource(ref);
    nav.openInstance(o.namespace, o.name);
  };
}

export function ApplyConflictNote({ cluster, text, conflict }: { cluster: string; text: string; conflict: ApplyConflictDTO }) {
  const top = conflict.owner ? conflict.owner.up ?? conflict.owner : undefined;
  const open = top ? ownerOpener(cluster, top) : null;
  return (
    <div data-testid="apply-conflict" style={{ display: "flex", flexDirection: "column", gap: 6, alignItems: "flex-start" }}>
      <span style={{ overflowWrap: "anywhere" }}>{text}</span>
      {top && open && (
        <button type="button" className="l-btn" data-testid="apply-conflict-owner" onClick={open}>
          Open {top.kind} {top.namespace ? `${top.namespace}/` : ""}{top.name}
        </button>
      )}
    </div>
  );
}
