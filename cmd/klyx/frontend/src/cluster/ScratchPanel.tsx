import { useEffect, useState } from "react";
import { useFleet, AdhocObjectDTO } from "../store/fleet";
import { listAdhocObjects, deleteAdhocObject } from "../bridge/crd";
import { launchDiagnosticPod } from "../bridge/pods";
import { armProtected } from "../bridge/configsvc";
import { btn } from "../chrome/primitives";
import { IconX, IconAlertTriangle } from "@tabler/icons-react";
import { vh, vw } from "../app/zoom";

// Image presets for the diagnostic-pod launcher. Curated, not a free-text field
// by default: the common debug images, with netshoot first (the network
// debugging swiss-army knife this fleet actually needs).
const DEBUG_IMAGES = ["nicolaka/netshoot", "busybox", "registry.k8s.io/e2e-test-images/jessie-dnsutils:1.7"];
const TTL_CHOICES: Array<{ label: string; seconds: number }> = [
  { label: "15m", seconds: 900 },
  { label: "1h", seconds: 3600 },
  { label: "4h", seconds: 14400 },
];

function ago(sec: number): string {
  if (sec < 60) return `${sec}s`;
  if (sec < 3600) return `${Math.floor(sec / 60)}m`;
  if (sec < 86400) return `${Math.floor(sec / 3600)}h`;
  return `${Math.floor(sec / 86400)}d`;
}

// ScratchPanel is the ad-hoc surface's home: launch a TTL-bounded diagnostic
// pod into klyx-scratch, and see + clean every object Klyx created ad-hoc
// (label klyx.dev/adhoc). The inventory is the honesty backstop for TTL'd
// objects - a deadline kills a pod but does not delete it; this list does.
export function ScratchPanel({ cluster, onClose, onLaunched }: { cluster: string; onClose: () => void; onLaunched?: (namespace: string) => void }) {
  const isProtected = useFleet((s) => s.clusters.find((c) => c.name === cluster)?.protected ?? false);
  const [objects, setObjects] = useState<AdhocObjectDTO[] | null>(null);
  const [busy, setBusy] = useState(false);
  const [typed, setTyped] = useState("");
  const [podName, setPodName] = useState("dbg");
  const [image, setImage] = useState(DEBUG_IMAGES[0]);
  const [ttl, setTtl] = useState(TTL_CHOICES[1].seconds);
  const armed = !isProtected || typed === cluster;

  const refresh = async () => setObjects(await listAdhocObjects(cluster));
  useEffect(() => { void refresh(); }, [cluster]); // eslint-disable-line react-hooks/exhaustive-deps

  const arm = async (): Promise<boolean> => {
    if (!isProtected) return true;
    const grant = await armProtected(cluster, typed);
    if (!grant.ok) {
      useFleet.getState().setActionStatus({ kind: "error", message: grant.error || `Confirmation mismatch for protected cluster ${cluster}` });
      return false;
    }
    return true;
  };

  const launch = async () => {
    setBusy(true);
    try {
      if (!(await arm())) return;
      const r = await launchDiagnosticPod(cluster, podName.trim(), image, ttl);
      if (r.error) {
        useFleet.getState().setActionStatus({ kind: "error", message: `Debug pod: ${r.error}` });
      } else {
        useFleet.getState().setActionStatus({ kind: "success", message: `Debug pod ${r.namespace}/${r.name} launched (dies in ${ago(ttl)})` });
        onLaunched?.(r.namespace);
        await refresh();
      }
    } finally {
      setBusy(false);
    }
  };

  const remove = async (o: AdhocObjectDTO) => {
    setBusy(true);
    try {
      if (!(await arm())) return;
      const r = await deleteAdhocObject(cluster, o);
      useFleet.getState().setActionStatus(r.ok
        ? { kind: "success", message: `Deleted scratch ${o.kind} ${o.namespace ? `${o.namespace}/` : ""}${o.name}` }
        : { kind: "error", message: `Delete failed: ${r.error}` });
      await refresh();
    } finally {
      setBusy(false);
    }
  };

  const removeAll = async () => {
    if (!objects || objects.length === 0) return;
    setBusy(true);
    try {
      let failed = 0;
      // Namespaces last: deleting klyx-scratch first would cascade its pods and
      // then per-object deletes hit honest not-found errors.
      const ordered = [...objects.filter((o) => o.kind !== "Namespace"), ...objects.filter((o) => o.kind === "Namespace")];
      for (const o of ordered) {
        if (!(await arm())) return; // protected: the one-shot grant re-arms per call
        const r = await deleteAdhocObject(cluster, o);
        if (!r.ok) failed++;
      }
      useFleet.getState().setActionStatus(failed === 0
        ? { kind: "success", message: `Scratch cleanup - ${ordered.length} object${ordered.length === 1 ? "" : "s"} deleted` }
        : { kind: "error", message: `Scratch cleanup - ${failed} of ${ordered.length} deletes failed` });
      await refresh();
    } finally {
      setBusy(false);
    }
  };

  return (
    <div role="dialog" aria-label="scratch objects" style={{ position: "fixed", inset: 0, background: "color-mix(in srgb, var(--color-background-primary) 72%, transparent)", display: "flex", alignItems: "center", justifyContent: "center", zIndex: 60 }}>
      <div style={{ width: 640, maxWidth: vw(92), maxHeight: vh(88), overflowY: "auto", background: "var(--color-background-primary)", border: "0.5px solid var(--color-border-secondary)", borderRadius: "var(--border-radius-md)", padding: 16, display: "flex", flexDirection: "column", gap: 12 }}>
        <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
          <div style={{ fontSize: 13, fontWeight: 600 }}>Scratch</div>
          <div style={{ fontSize: 11, color: "var(--color-text-tertiary)" }}>ad-hoc objects Klyx created (klyx.dev/adhoc) - not in Git</div>
          <div style={{ flex: 1 }} />
          <button onClick={onClose} aria-label="close scratch panel" style={{ ...btn, display: "inline-flex", alignItems: "center" }}><IconX size={13} stroke={1.5} /></button>
        </div>

        {/* Launcher: a TTL-bounded debug pod into klyx-scratch. */}
        <div style={{ display: "flex", alignItems: "center", gap: 6, flexWrap: "wrap", padding: "8px 10px", borderRadius: 4, border: "0.5px solid var(--color-border-tertiary)", background: "var(--color-background-secondary)" }}>
          <input value={podName} onChange={(e) => setPodName(e.target.value)} aria-label="debug pod name" style={{ fontFamily: "var(--font-mono)", fontSize: 11, padding: "4px 8px", width: 90, background: "var(--color-background-primary)", border: "0.5px solid var(--color-border-tertiary)", borderRadius: 4, color: "var(--color-text-primary)" }} />
          <select value={image} onChange={(e) => setImage(e.target.value)} aria-label="debug pod image" style={{ fontSize: 11, padding: "4px 6px", background: "var(--color-background-primary)", border: "0.5px solid var(--color-border-tertiary)", borderRadius: 4, color: "var(--color-text-primary)", maxWidth: 220 }}>
            {DEBUG_IMAGES.map((i) => <option key={i} value={i}>{i}</option>)}
          </select>
          <select value={ttl} onChange={(e) => setTtl(Number(e.target.value))} aria-label="debug pod ttl" style={{ fontSize: 11, padding: "4px 6px", background: "var(--color-background-primary)", border: "0.5px solid var(--color-border-tertiary)", borderRadius: 4, color: "var(--color-text-primary)" }}>
            {TTL_CHOICES.map((t) => <option key={t.seconds} value={t.seconds}>{t.label}</option>)}
          </select>
          <button onClick={() => void launch()} disabled={busy || podName.trim() === "" || !armed} style={{ ...btn, opacity: armed ? 1 : 0.5 }} title={armed ? `Launch into ${"klyx-scratch"} (cluster kills it after the TTL)` : "Type the cluster name below to arm"}>
            {busy ? "…" : "launch debug pod"}
          </button>
          <span style={{ fontSize: 10, color: "var(--color-text-tertiary)" }}>→ klyx-scratch · cluster-enforced TTL</span>
        </div>

        {isProtected && (
          <div style={{ display: "flex", alignItems: "center", gap: 6 }}>
            <IconAlertTriangle size={12} stroke={1.5} style={{ color: "var(--color-text-warning)" }} />
            <input
              value={typed}
              onChange={(e) => setTyped(e.target.value)}
              placeholder={`type "${cluster}" to arm launch/delete`}
              style={{ fontFamily: "var(--font-mono)", fontSize: 11, padding: "4px 8px", width: 220, background: "var(--color-background-secondary)", border: "0.5px solid var(--color-border-warning)", borderRadius: 4, color: "var(--color-text-primary)" }}
            />
          </div>
        )}

        <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
          <div style={{ fontSize: 10, textTransform: "uppercase", letterSpacing: 0.5, color: "var(--color-text-tertiary)" }}>
            objects {objects ? `(${objects.length})` : ""}
          </div>
          <div style={{ flex: 1 }} />
          <button onClick={() => void refresh()} disabled={busy} style={btn}>refresh</button>
          <button onClick={() => void removeAll()} disabled={busy || !objects || objects.length === 0 || !armed} style={{ ...btn, color: "var(--color-text-danger)", borderColor: "var(--color-border-danger)", background: "var(--color-background-danger)", opacity: objects && objects.length > 0 && armed ? 1 : 0.5 }}>
            delete all
          </button>
        </div>

        {objects === null ? (
          <div style={{ fontSize: 11, color: "var(--color-text-tertiary)" }}>loading…</div>
        ) : objects.length === 0 ? (
          <div style={{ fontSize: 11, color: "var(--color-text-tertiary)" }}>No scratch objects on {cluster}. Everything Klyx creates ad-hoc shows up here.</div>
        ) : (
          <div style={{ display: "flex", flexDirection: "column", gap: 4 }}>
            {objects.map((o) => (
              <div key={`${o.plural}/${o.namespace}/${o.name}`} style={{ display: "flex", alignItems: "center", gap: 8, padding: "5px 9px", borderRadius: 4, border: "0.5px solid var(--color-border-tertiary)", background: "var(--color-background-secondary)", fontFamily: "var(--font-mono)", fontSize: 11 }}>
                <span style={{ color: "var(--color-text-tertiary)", width: 110, flexShrink: 0 }}>{o.kind}</span>
                <span style={{ overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>{o.namespace ? `${o.namespace}/` : ""}{o.name}</span>
                <div style={{ flex: 1 }} />
                <span style={{ color: "var(--color-text-tertiary)", flexShrink: 0 }}>{ago(o.ageSeconds)}</span>
                <button onClick={() => void remove(o)} disabled={busy || !armed} style={{ ...btn, fontSize: 10, color: "var(--color-text-danger)", borderColor: "var(--color-border-danger)", opacity: armed ? 1 : 0.5 }}>delete</button>
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}
