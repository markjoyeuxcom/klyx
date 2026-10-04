import { useState } from "react";
import { useFleet, AdhocResultDTO } from "../store/fleet";
import { createResourceYAML } from "../bridge/crd";
import { armProtected } from "../bridge/configsvc";
import { btn } from "../chrome/primitives";
import { IconAlertTriangle, IconX } from "@tabler/icons-react";

// AdhocComposer is the deliberately-non-GitOps scratch/deploy surface: paste a
// manifest (multi-doc ok), dry-run it against the server, review the per-doc
// verdicts - INCLUDING the red "this namespace belongs to a pruning GitOps unit,
// your object will be DELETED on the next reconcile" warning - then create.
// Everything it makes is stamped klyx.dev/adhoc. This is for diagnostic scratch
// objects, NOT for authoring your app: desired state belongs in Git.
//
// Flow honesty: "create" is enabled only after a clean dry-run of the CURRENT
// text; any edit invalidates the dry-run. Protected clusters require the typed
// cluster name (the same one-shot arming every destructive verb uses).
export function AdhocComposer({ cluster, onClose }: { cluster: string; onClose: () => void }) {
  const isProtected = useFleet((s) => s.clusters.find((c) => c.name === cluster)?.protected ?? false);
  const [yaml, setYaml] = useState("");
  const [results, setResults] = useState<AdhocResultDTO[] | null>(null);
  const [checkedYaml, setCheckedYaml] = useState<string | null>(null); // the text the last dry-run validated
  const [busy, setBusy] = useState<"dry-run" | "create" | null>(null);
  const [typed, setTyped] = useState("");

  const dryRunClean = results !== null && checkedYaml === yaml && results.every((r) => r.ok);
  const armed = !isProtected || typed === cluster;

  const runDry = async () => {
    setBusy("dry-run");
    try {
      const rs = await createResourceYAML(cluster, yaml, true);
      setResults(rs);
      setCheckedYaml(yaml);
    } finally {
      setBusy(null);
    }
  };

  const runCreate = async () => {
    setBusy("create");
    try {
      if (isProtected) {
        const grant = await armProtected(cluster, typed);
        if (!grant.ok) {
          useFleet.getState().setActionStatus({ kind: "error", message: grant.error || `Confirmation mismatch for protected cluster ${cluster}` });
          return;
        }
      }
      const rs = await createResourceYAML(cluster, yaml, false);
      setResults(rs);
      setCheckedYaml(null); // a fresh dry-run is required before any re-create
      const okCount = rs.filter((r) => r.ok).length;
      const failCount = rs.length - okCount;
      useFleet.getState().setActionStatus(
        failCount === 0
          ? { kind: "success", message: `Ad-hoc create — ${okCount} object${okCount === 1 ? "" : "s"} on ${cluster} (labeled klyx.dev/adhoc)` }
          : { kind: "error", message: `Ad-hoc create — ${okCount} ok, ${failCount} failed on ${cluster}` },
      );
    } finally {
      setBusy(null);
    }
  };

  return (
    <div role="dialog" aria-label="deploy ad-hoc yaml" style={{ position: "fixed", inset: 0, background: "color-mix(in srgb, var(--color-background-primary) 72%, transparent)", display: "flex", alignItems: "center", justifyContent: "center", zIndex: 60 }}>
      <div style={{ width: 700, maxWidth: "92vw", maxHeight: "88vh", overflowY: "auto", background: "var(--color-background-primary)", border: "0.5px solid var(--color-border-secondary)", borderRadius: "var(--border-radius-md)", padding: 16, display: "flex", flexDirection: "column", gap: 10 }}>
        <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
          <div style={{ fontSize: 13, fontWeight: 600 }}>Deploy ad-hoc YAML</div>
          <div style={{ flex: 1 }} />
          <button onClick={onClose} aria-label="close composer" style={{ ...btn, display: "inline-flex", alignItems: "center" }}><IconX size={13} stroke={1.5} /></button>
        </div>
        <div style={{ fontSize: 11, color: "var(--color-text-secondary)" }}>
          Scratch/diagnostic objects only — everything is labeled <span style={{ fontFamily: "var(--font-mono)" }}>klyx.dev/adhoc</span> and is <b>not</b> in Git.
          Author real desired state as Helm/Kustomize in your repo. Secrets are refused here (use ESO/SOPS).
        </div>
        <textarea
          value={yaml}
          onChange={(e) => setYaml(e.target.value)}
          placeholder={"apiVersion: v1\nkind: Pod\nmetadata:\n  name: dbg\n  namespace: klyx-scratch\nspec:\n  containers:\n  - name: sh\n    image: nicolaka/netshoot\n    command: [sleep, \"3600\"]"}
          spellCheck={false}
          style={{ fontFamily: "var(--font-mono)", fontSize: 12, minHeight: 240, resize: "vertical", padding: 10, background: "var(--color-background-secondary)", color: "var(--color-text-primary)", border: "0.5px solid var(--color-border-tertiary)", borderRadius: 4, lineHeight: 1.5 }}
        />

        {results && (
          <div style={{ display: "flex", flexDirection: "column", gap: 6 }}>
            {results.map((r, i) => (
              <div key={i} style={{ display: "flex", flexDirection: "column", gap: 3, padding: "6px 9px", borderRadius: 4, border: `0.5px solid var(--color-border-${r.ok ? "tertiary" : "danger"})`, background: "var(--color-background-secondary)" }}>
                <div style={{ display: "flex", gap: 8, fontFamily: "var(--font-mono)", fontSize: 11 }}>
                  <span style={{ color: r.ok ? "var(--color-text-success)" : "var(--color-text-danger)" }}>{r.ok ? "ok" : "fail"}</span>
                  <span>{r.kind || "?"} {r.namespace ? `${r.namespace}/` : ""}{r.name || ""}</span>
                </div>
                {r.error && <div style={{ fontSize: 11, color: "var(--color-text-danger)", overflowWrap: "anywhere" }}>{r.error}</div>}
                {r.ok && (r.namespaceOwnerKind !== "" || r.argoTracking !== "") && (
                  <NamespaceContextNote r={r} />
                )}
              </div>
            ))}
          </div>
        )}

        <div style={{ display: "flex", alignItems: "center", gap: 8, flexWrap: "wrap" }}>
          <button onClick={() => void runDry()} disabled={busy !== null || yaml.trim() === ""} style={btn}>
            {busy === "dry-run" ? "checking…" : "dry-run"}
          </button>
          {isProtected && dryRunClean && (
            <input
              value={typed}
              onChange={(e) => setTyped(e.target.value)}
              placeholder={`type "${cluster}" to arm`}
              style={{ fontFamily: "var(--font-mono)", fontSize: 11, padding: "4px 8px", width: 180, background: "var(--color-background-secondary)", border: "0.5px solid var(--color-border-warning)", borderRadius: 4, color: "var(--color-text-primary)" }}
            />
          )}
          <button
            onClick={() => void runCreate()}
            disabled={busy !== null || !dryRunClean || !armed}
            title={dryRunClean ? (armed ? "Create the objects (labeled klyx.dev/adhoc)" : "Type the cluster name to arm") : "Run a clean dry-run of the current text first"}
            style={{ ...btn, color: "var(--color-text-danger)", borderColor: "var(--color-border-danger)", background: "var(--color-background-danger)", opacity: dryRunClean && armed ? 1 : 0.5, cursor: dryRunClean && armed && busy === null ? "pointer" : "not-allowed" }}
          >
            {busy === "create" ? "creating…" : "create"}
          </button>
          <span style={{ fontSize: 10, color: "var(--color-text-tertiary)" }}>
            dry-run first · edits re-require it{isProtected ? " · prd locked" : ""}
          </span>
        </div>
      </div>
    </div>
  );
}

// NamespaceContextNote states what the namespace's GitOps units can actually
// do to this object (review 2026-10-03 #13, batch-4 R2). Flux garbage-collects
// only its own inventory and Argo prunes only what it tracks, so reaching the
// namespace is context, not a deletion verdict. Certain pruning needs an Argo
// app that tracks the object under its KNOWN tracking method and prunes
// automatically; with the method unknown, matching metadata is only a
// possibility. A unit managing the namespace itself is a conditional risk.
export function NamespaceContextNote({ r }: { r: AdhocResultDTO }) {
  const unit = `${r.namespaceOwnerKind} ${r.namespaceOwnerNamespace ? `${r.namespaceOwnerNamespace}/` : ""}${r.namespaceOwnerName}`;
  const app = `Argo app ${r.argoTrackingApp}`;
  const certain = r.argoTracking === "tracked" && r.argoTrackingPrunes;
  const parts: string[] = [];
  if (r.argoTracking === "tracked") {
    parts.push(r.argoTrackingPrunes
      ? `${app} tracks this object (its tracking metadata matches the controller's tracking method) and prunes automatically: the object is not in Git, so the next sync deletes it.`
      : `${app} tracks this object (its tracking metadata matches the controller's tracking method): the app shows it OutOfSync, and a sync with pruning would delete it.`);
  } else if (r.argoTracking === "possible") {
    parts.push(`This object carries ${app}'s tracking metadata. Whether Argo treats it as one of the app's resources depends on the controller's tracking method, which Klyx could not read; if it does, a sync with pruning deletes it.`);
  } else if (r.namespaceOwnerKind === "Kustomization") {
    parts.push(`Namespace ${r.namespace} is deployed into by ${unit}. This new object is not in its inventory, so its pruning leaves the object alone.`);
  } else if (r.namespaceOwnerKind !== "") {
    parts.push(r.argoConfigKnown
      ? `Namespace ${r.namespace} is deployed into by ${unit}. This object carries none of its tracking metadata, so its pruning leaves the object alone.`
      : `Namespace ${r.namespace} is deployed into by ${unit}. This object carries no tracking metadata Klyx recognizes (the controller's tracking settings could not be read), so its pruning should leave the object alone.`);
  }
  if (r.namespaceOwnerKind !== "" && r.namespaceManaged) {
    parts.push(r.namespacePrunes
      ? `${unit} also manages the namespace itself: if the namespace is removed from Git, it is deleted together with this object.`
      : `${unit} manages the namespace itself but does not prune it.`);
  }
  if (parts.length === 0) return null;
  parts.push("An unmanaged namespace like klyx-scratch avoids GitOps interplay altogether.");
  return (
    <div data-testid="namespace-context" style={{ display: "flex", alignItems: "flex-start", gap: 5, fontSize: 11, color: certain ? "var(--color-text-danger)" : "var(--color-text-warning)" }}>
      <IconAlertTriangle size={12} stroke={1.5} style={{ flexShrink: 0, marginTop: 1 }} />
      <span>{parts.join(" ")}</span>
    </div>
  );
}
