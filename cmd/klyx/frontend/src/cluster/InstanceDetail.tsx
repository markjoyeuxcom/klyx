import { useEffect, useRef, useState } from "react";
import { useFleet, ResourceRef, InstanceRef, InstanceDetailDTO, SecretKeyDTO, ServiceBackingDTO, HPAScalingDTO, RelatedRefDTO, EventDTO, FactDTO, AIResultDTO, GitOpsOwnershipDTO } from "../store/fleet";
import { getInstanceDetail, revealSecretKey, copyText, dryRunInstanceYAML, applyInstanceYAML, gitOpsOwnership, ActionResult } from "../bridge/crd";
import { explainResource, suggestFix } from "../bridge/ai";
import { useAIAvailability } from "./useAI";
import { armProtected } from "../bridge/configsvc";
import { openPodDetail } from "../bridge/pods";
import { ForwardPopover } from "./ForwardPopover";
import { InspectorStatus, conditionColor, InspectorConditions, InspectorEvents, InspectorSection, InspectorTabButton, InspectorFacts, InspectorKV, InspectorMeters, MetadataChips, inspectorStateBox } from "./ResourceInspector";
import { actionBtn, ellipsis, type Tone } from "../chrome/primitives";
import { CopyButton } from "../chrome/CopyButton";
import { DescribePane } from "../chrome/DescribePane";
import { IconX, IconSparkles, IconAlertTriangle } from "@tabler/icons-react";

type DetailTab = "summary" | "related" | "events" | "yaml";

const TONE_SET = new Set<string>(["success", "warning", "danger", "info", "muted", "neutral"]);
function asTone(s: string): Tone | undefined {
  return TONE_SET.has(s) ? (s as Tone) : undefined;
}

// FactsSummary renders a resource's salient facts (the data path) grouped by
// section: wells (short scalars/counts) as counter wells, rows (lists/prose) as
// labeled kv rows. Group "" is the headline block (no header). Every kind with a
// registered backend fact builder renders here with zero bespoke code - the
// generic path that replaces the old "labels/annotations only" summary.
function FactsSummary({ facts }: { facts?: FactDTO[] }) {
  if (!facts || facts.length === 0) return null;
  const order: string[] = [];
  const byGroup = new Map<string, FactDTO[]>();
  for (const f of facts) {
    if (!byGroup.has(f.group)) {
      byGroup.set(f.group, []);
      order.push(f.group);
    }
    byGroup.get(f.group)!.push(f);
  }
  return (
    <>
      {order.map((group) => {
        const items = byGroup.get(group)!;
        const wells = items.filter((f) => f.kind === "well").map((f) => ({ label: f.label, value: f.value, tone: asTone(f.tone) }));
        const meters = items.filter((f) => f.kind === "meter").map((f) => ({ label: f.label, value: f.value, pct: f.pct ?? 0, tone: asTone(f.tone) }));
        const rows = items.filter((f) => f.kind !== "well" && f.kind !== "meter").map((f) => ({ label: f.label, value: f.value, tone: asTone(f.tone) }));
        const blocks = [wells.length > 0, meters.length > 0, rows.length > 0].filter(Boolean).length;
        const body = (
          <div style={{ display: "flex", flexDirection: "column", gap: blocks > 1 ? 8 : 0 }}>
            <InspectorFacts facts={wells} />
            <InspectorMeters meters={meters} />
            <InspectorKV rows={rows} />
          </div>
        );
        return group === "" ? (
          <div key="__headline" style={{ marginBottom: 8 }}>{body}</div>
        ) : (
          <InspectorSection key={group} title={group} collapsible>{body}</InspectorSection>
        );
      })}
    </>
  );
}

function age(created: string): string {
  if (!created) return "";
  const ms = Date.now() - Date.parse(created);
  if (Number.isNaN(ms) || ms < 0) return "";
  const sec = Math.floor(ms / 1000);
  if (sec < 60) return `${sec}s`;
  if (sec < 3600) return `${Math.floor(sec / 60)}m`;
  if (sec < 86400) return `${Math.floor(sec / 3600)}h`;
  return `${Math.floor(sec / 86400)}d`;
}

type GitOwner = { tool: "Argo CD" | "Flux" | "Helm"; detail: string };
type DiffLine = { type: "context" | "added" | "removed"; text: string; oldLine?: number; newLine?: number };
type LineDiff = { added: number; removed: number; lines: DiffLine[] };
type FoldedDiffRow = { kind: "line"; line: DiffLine } | { kind: "fold"; id: string; hidden: number; lines: DiffLine[] };
type ValidationIssue = { tone: "danger" | "warning"; message: string; line?: number; column?: number };
type YAMLIdentity = { apiVersion?: string; kind?: string; metadataName?: string; metadataNamespace?: string; metadataResourceVersion?: string };
type YAMLValidation = { blocking: boolean; issues: ValidationIssue[]; identity: YAMLIdentity };
type CursorPosition = { line: number; column: number };

export function detectGitOwner(labels: Record<string, string> = {}, annotations: Record<string, string> = {}): GitOwner | null {
  if (labels["argocd.argoproj.io/instance"]) {
    return { tool: "Argo CD", detail: labels["argocd.argoproj.io/instance"] };
  }
  if (annotations["argocd.argoproj.io/tracking-id"]) {
    return { tool: "Argo CD", detail: annotations["argocd.argoproj.io/tracking-id"] };
  }
  if (labels["kustomize.toolkit.fluxcd.io/name"] || annotations["kustomize.toolkit.fluxcd.io/name"]) {
    const name = labels["kustomize.toolkit.fluxcd.io/name"] ?? annotations["kustomize.toolkit.fluxcd.io/name"];
    const ns = labels["kustomize.toolkit.fluxcd.io/namespace"] ?? annotations["kustomize.toolkit.fluxcd.io/namespace"];
    return { tool: "Flux", detail: ns ? `${ns}/${name}` : name };
  }
  if (labels["helm.toolkit.fluxcd.io/name"] || annotations["helm.toolkit.fluxcd.io/name"]) {
    const name = labels["helm.toolkit.fluxcd.io/name"] ?? annotations["helm.toolkit.fluxcd.io/name"];
    const ns = labels["helm.toolkit.fluxcd.io/namespace"] ?? annotations["helm.toolkit.fluxcd.io/namespace"];
    return { tool: "Flux", detail: ns ? `${ns}/${name}` : name };
  }
  if (labels["app.kubernetes.io/managed-by"] === "Helm" || annotations["meta.helm.sh/release-name"]) {
    const release = annotations["meta.helm.sh/release-name"] ?? labels["app.kubernetes.io/instance"] ?? "Helm release";
    const ns = annotations["meta.helm.sh/release-namespace"];
    return { tool: "Helm", detail: ns ? `${ns}/${release}` : release };
  }
  return null;
}

export function buildLineDiff(before: string, after: string): LineDiff {
  const oldLines = before.split("\n");
  const newLines = after.split("\n");
  let start = 0;
  while (start < oldLines.length && start < newLines.length && oldLines[start] === newLines[start]) start++;

  let oldEnd = oldLines.length - 1;
  let newEnd = newLines.length - 1;
  while (oldEnd >= start && newEnd >= start && oldLines[oldEnd] === newLines[newEnd]) {
    oldEnd--;
    newEnd--;
  }

  const lines: DiffLine[] = [];
  for (let i = 0; i < start; i++) {
    lines.push({ type: "context", text: oldLines[i], oldLine: i + 1, newLine: i + 1 });
  }
  for (let i = start; i <= oldEnd; i++) {
    lines.push({ type: "removed", text: oldLines[i], oldLine: i + 1 });
  }
  for (let i = start; i <= newEnd; i++) {
    lines.push({ type: "added", text: newLines[i], newLine: i + 1 });
  }
  const oldTailStart = oldEnd + 1;
  const newTailStart = newEnd + 1;
  const tailCount = oldLines.length - oldTailStart;
  for (let i = 0; i < tailCount; i++) {
    lines.push({
      type: "context",
      text: oldLines[oldTailStart + i],
      oldLine: oldTailStart + i + 1,
      newLine: newTailStart + i + 1,
    });
  }

  return { added: Math.max(0, newEnd - start + 1), removed: Math.max(0, oldEnd - start + 1), lines };
}

export function buildFoldedDiffRows(diff: LineDiff, expandedIds: ReadonlySet<string>, contextLines = 3, foldThreshold = 8): FoldedDiffRow[] {
  const rows: FoldedDiffRow[] = [];
  let cursor = 0;

  while (cursor < diff.lines.length) {
    const line = diff.lines[cursor];
    if (line.type !== "context") {
      rows.push({ kind: "line", line });
      cursor++;
      continue;
    }

    const start = cursor;
    while (cursor < diff.lines.length && diff.lines[cursor].type === "context") cursor++;
    const run = diff.lines.slice(start, cursor);
    const previousIsChange = start > 0 && diff.lines[start - 1].type !== "context";
    const nextIsChange = cursor < diff.lines.length && diff.lines[cursor].type !== "context";

    if (run.length <= foldThreshold || (!previousIsChange && !nextIsChange)) {
      rows.push(...run.map((contextLine) => ({ kind: "line" as const, line: contextLine })));
      continue;
    }

    const leadingCount = previousIsChange ? contextLines : 0;
    const trailingCount = nextIsChange ? contextLines : 0;
    const visibleCount = Math.min(run.length, leadingCount + trailingCount);
    const hiddenCount = run.length - visibleCount;
    if (hiddenCount <= 0) {
      rows.push(...run.map((contextLine) => ({ kind: "line" as const, line: contextLine })));
      continue;
    }

    const foldStart = leadingCount;
    const foldEnd = run.length - trailingCount;
    const hiddenLines = run.slice(foldStart, foldEnd);
    const firstHidden = hiddenLines[0];
    const lastHidden = hiddenLines[hiddenLines.length - 1];
    const id = `${firstHidden?.oldLine ?? firstHidden?.newLine ?? start}-${lastHidden?.oldLine ?? lastHidden?.newLine ?? cursor}`;
    const expanded = expandedIds.has(id);
    rows.push(...run.slice(0, foldStart).map((contextLine) => ({ kind: "line" as const, line: contextLine })));
    if (expanded) {
      rows.push(...hiddenLines.map((contextLine) => ({ kind: "line" as const, line: contextLine })));
    } else {
      rows.push({ kind: "fold", id, hidden: hiddenCount, lines: hiddenLines });
    }
    rows.push(...run.slice(foldEnd).map((contextLine) => ({ kind: "line" as const, line: contextLine })));
  }

  return rows;
}

export function formatDiffPlainText(diff: LineDiff, target: string): string {
  const lines = [
    `YAML diff for ${target}`,
    `+${diff.added} -${diff.removed}`,
    "",
  ];
  for (const line of diff.lines) {
    const oldLine = line.oldLine === undefined ? "".padStart(5, " ") : String(line.oldLine).padStart(5, " ");
    const newLine = line.newLine === undefined ? "".padStart(5, " ") : String(line.newLine).padStart(5, " ");
    const marker = line.type === "added" ? "+" : line.type === "removed" ? "-" : " ";
    lines.push(`${oldLine} ${newLine} ${marker} ${line.text}`);
  }
  return lines.join("\n");
}

export function formatKubernetesActionError(raw: string): string {
  const error = raw.trim().replace(/\s+/g, " ");
  if (!error) return "Kubernetes rejected the request.";

  const webhookTimeout = /failed calling webhook "([^"]+)": .*?(context deadline exceeded|timeout|timed out)/i.exec(error);
  if (webhookTimeout) {
    return `Admission webhook timed out: ${webhookTimeout[1]}. This is usually transient; retry after the webhook/controller recovers.`;
  }

  const admission = /admission webhook "([^"]+)" denied the request: (.+)$/i.exec(error);
  if (admission) {
    return `Admission denied by ${admission[1]}: ${admission[2]}`;
  }

  const forbidden = /User "([^"]+)" cannot ([a-z]+) resource "([^"]+)"(?: in API group "([^"]*)")?(?: in the namespace "([^"]+)")?/i.exec(error);
  if (forbidden) {
    const [, user, verb, resourceName, group, namespace] = forbidden;
    const resourceNameWithGroup = group ? `${resourceName}.${group}` : resourceName;
    const scope = namespace ? ` in namespace ${namespace}` : "";
    return `Permission denied: ${user} cannot ${verb} ${resourceNameWithGroup}${scope}. Check RBAC for this cluster context.`;
  }

  if (/the object has been modified|Operation cannot be fulfilled|409 Conflict/i.test(error)) {
    return "Conflict: live object changed while editing. Review the latest YAML, then reapply.";
  }

  if (/field is immutable|immutable field/i.test(error)) {
    return `Immutable field: Kubernetes rejected this because one or more fields cannot be changed after creation. ${extractInvalidDetail(error)}`;
  }

  if (/dry run is not supported|does not support dry run/i.test(error)) {
    return "Server dry-run is not supported for this resource or API path. Apply is still blocked until the server can validate it another way.";
  }

  const invalid = /\bis invalid: (.+)$/i.exec(error);
  if (invalid) {
    return `Field validation failed: ${invalid[1]}`;
  }

  return error;
}

function extractInvalidDetail(error: string): string {
  const invalid = /\bis invalid: (.+)$/i.exec(error);
  return invalid?.[1] ?? error;
}

export function cursorPositionForOffset(text: string, offset: number): CursorPosition {
  const safeOffset = Math.max(0, Math.min(offset, text.length));
  const before = text.slice(0, safeOffset);
  const lineBreaks = before.match(/\n/g)?.length ?? 0;
  const lastBreak = before.lastIndexOf("\n");
  return { line: lineBreaks + 1, column: safeOffset - lastBreak };
}

function lineOffset(text: string, line: number, column = 1): number {
  const safeLine = Math.max(1, line);
  const safeColumn = Math.max(1, column);
  let offset = 0;
  for (let currentLine = 1; currentLine < safeLine; currentLine++) {
    const nextBreak = text.indexOf("\n", offset);
    if (nextBreak < 0) return text.length;
    offset = nextBreak + 1;
  }
  return Math.min(text.length, offset + safeColumn - 1);
}

function lineEndOffset(text: string, offset: number): number {
  const nextBreak = text.indexOf("\n", offset);
  return nextBreak < 0 ? text.length : nextBreak;
}

export function validateResourceYAML(yaml: string, resource: ResourceRef, instance: InstanceRef): YAMLValidation {
  const issues: ValidationIssue[] = [];
  const identity: YAMLIdentity = {};
  const lines = yaml.split("\n");
  let inMetadata = false;

  for (let i = 0; i < lines.length; i++) {
    const raw = lines[i];
    const lineNo = i + 1;
    if (/^\s*$/.test(raw) || /^\s*#/.test(raw) || raw.trim() === "---" || raw.trim() === "...") continue;
    const tabIndex = raw.indexOf("\t");
    if (tabIndex >= 0) {
      issues.push({ tone: "danger", line: lineNo, column: tabIndex + 1, message: "YAML parse error: tabs cannot be used for indentation" });
      continue;
    }

    const indent = raw.length - raw.trimStart().length;
    const trimmed = raw.trim();
    if (indent === 0) {
      inMetadata = false;
      const topMatch = /^([A-Za-z0-9_.-]+):(.*)$/.exec(trimmed);
      if (!topMatch) {
        issues.push({ tone: "danger", line: lineNo, column: 1, message: "YAML parse error: expected key: value" });
        continue;
      }
      const key = topMatch[1];
      const value = cleanYamlScalar(topMatch[2]);
      if (key === "apiVersion") identity.apiVersion = value;
      if (key === "kind") identity.kind = value;
      if (key === "metadata") inMetadata = true;
      continue;
    }

    if (inMetadata) {
      const metadataMatch = /^([A-Za-z0-9_.-]+):(.*)$/.exec(trimmed);
      if (!metadataMatch) {
        issues.push({ tone: "danger", line: lineNo, column: indent + 1, message: "YAML parse error in metadata: expected key: value" });
        continue;
      }
      const key = metadataMatch[1];
      const value = cleanYamlScalar(metadataMatch[2]);
      if (key === "name") identity.metadataName = value;
      if (key === "namespace") identity.metadataNamespace = value;
      if (key === "resourceVersion") identity.metadataResourceVersion = value;
    }
  }

  if (!identity.apiVersion) issues.push({ tone: "danger", message: "Missing required field: apiVersion" });
  if (!identity.kind) issues.push({ tone: "danger", message: "Missing required field: kind" });
  if (!identity.metadataName) issues.push({ tone: "danger", message: "Missing required field: metadata.name" });

  const expectedApiVersion = resource.group ? `${resource.group}/${resource.version}` : resource.version;
  if (identity.apiVersion && identity.apiVersion !== expectedApiVersion) {
    issues.push({ tone: "warning", message: `apiVersion changed from ${expectedApiVersion} to ${identity.apiVersion}` });
  }
  if (identity.kind && identity.kind !== resource.kind) {
    issues.push({ tone: "warning", message: `kind changed from ${resource.kind} to ${identity.kind}` });
  }
  if (identity.metadataName && identity.metadataName !== instance.name) {
    issues.push({ tone: "warning", message: `metadata.name changed from ${instance.name} to ${identity.metadataName}` });
  }
  if (resource.scope === "Namespaced") {
    if (!identity.metadataNamespace) {
      issues.push({ tone: "warning", message: `metadata.namespace is empty; this object currently lives in ${instance.namespace || "default"}` });
    } else if (instance.namespace && identity.metadataNamespace !== instance.namespace) {
      issues.push({ tone: "warning", message: `metadata.namespace changed from ${instance.namespace} to ${identity.metadataNamespace}` });
    }
  } else if (identity.metadataNamespace) {
    issues.push({ tone: "warning", message: "metadata.namespace is set on a cluster-scoped resource" });
  }

  return { blocking: issues.some((issue) => issue.tone === "danger"), issues, identity };
}

function cleanYamlScalar(raw: string): string {
  const trimmed = raw.trim();
  if (!trimmed || trimmed === "{}" || trimmed === "null" || trimmed === "~") return "";
  const withoutComment = trimmed.startsWith("\"") || trimmed.startsWith("'")
    ? trimmed
    : trimmed.replace(/\s+#.*$/, "").trim();
  if (
    (withoutComment.startsWith("\"") && withoutComment.endsWith("\"")) ||
    (withoutComment.startsWith("'") && withoutComment.endsWith("'"))
  ) {
    return withoutComment.slice(1, -1);
  }
  return withoutComment;
}

// SecretDataSection renders the data section for a v1 Secret. Each key row
// shows the key name, byte count, masked/revealed value, reveal toggle, and
// a copy-without-reveal button. Revealed values are local to this component
// and are cleared on unmount.
function SecretDataSection({
  cluster, ns, name, keys,
}: {
  cluster: string; ns: string; name: string; keys: SecretKeyDTO[];
}) {
  // revealedValues: key -> decoded string. Never pushed to the store.
  const [revealedValues, setRevealedValues] = useState<Record<string, string>>({});
  // copyStates: key -> "idle" | "copied" | "error"
  const [copyStates, setCopyStates] = useState<Record<string, string>>({});
  // revealErrors: key -> error message
  const [revealErrors, setRevealErrors] = useState<Record<string, string>>({});

  // Clear revealed values on unmount (navigation away).
  const revealedRef = useRef(revealedValues);
  revealedRef.current = revealedValues;

  const handleReveal = async (key: string) => {
    if (revealedValues[key] !== undefined) {
      // Re-mask: remove from state.
      setRevealedValues((prev) => {
        const next = { ...prev };
        delete next[key];
        return next;
      });
      setRevealErrors((prev) => {
        const next = { ...prev };
        delete next[key];
        return next;
      });
      return;
    }
    const val = await revealSecretKey(cluster, ns, name, key);
    if (val === null) {
      setRevealErrors((prev) => ({ ...prev, [key]: "Could not fetch value" }));
      return;
    }
    setRevealErrors((prev) => {
      const next = { ...prev };
      delete next[key];
      return next;
    });
    setRevealedValues((prev) => ({ ...prev, [key]: val }));
  };

  const handleCopy = async (key: string) => {
    // Always fetch fresh — do not read from component state to keep the
    // flow explicit and avoid clipboard accidents from stale state.
    const val = await revealSecretKey(cluster, ns, name, key);
    if (val === null) {
      setCopyStates((prev) => ({ ...prev, [key]: "error" }));
      setTimeout(() => setCopyStates((prev) => ({ ...prev, [key]: "idle" })), 1500);
      return;
    }
    await copyText(val);
    setCopyStates((prev) => ({ ...prev, [key]: "copied" }));
    setTimeout(() => setCopyStates((prev) => ({ ...prev, [key]: "idle" })), 1500);
  };

  return (
    <InspectorSection title="Data" count={keys.length} collapsible>
      {keys.map(({ key, bytes }) => {
        const revealed = revealedValues[key];
        const isRevealed = revealed !== undefined;
        const copyState = copyStates[key] ?? "idle";
        const errMsg = revealErrors[key];
        return (
          <div key={key} style={{ display: "flex", flexDirection: "column", gap: 2, padding: "4px 0", borderBottom: "0.5px solid var(--color-border-tertiary)" }}>
            <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
              <span style={{ fontFamily: "var(--font-mono)", fontSize: 12, fontWeight: 500, flex: "0 0 auto", maxWidth: 220, ...ellipsis }} title={key}>{key}</span>
              <span style={{ fontSize: 11, color: "var(--color-text-tertiary)", flex: "0 0 auto" }}>{bytes} bytes</span>
              <div style={{ flex: 1 }} />
              <button
                onClick={() => void handleReveal(key)}
                style={{ ...btnSm }}
                title={isRevealed ? "Hide value" : "Reveal value"}
              >
                {isRevealed ? "Hide" : "Reveal"}
              </button>
              <button
                onClick={() => void handleCopy(key)}
                style={{ ...btnSm }}
                title="Copy decoded value to clipboard (no display)"
              >
                {copyState === "copied" ? "Copied" : copyState === "error" ? "Error" : "Copy"}
              </button>
            </div>
            {errMsg && (
              <div style={{ fontSize: 11, color: "var(--color-text-danger)", marginLeft: 4 }}>{errMsg}</div>
            )}
            {isRevealed && (
              <pre style={{
                margin: 0, padding: "6px 8px",
                background: "var(--color-background-secondary)",
                border: "0.5px solid var(--color-border-tertiary)",
                borderRadius: "var(--border-radius-md)",
                fontFamily: "var(--font-mono)", fontSize: 11, lineHeight: 1.5,
                whiteSpace: "pre-wrap", wordBreak: "break-all",
                maxHeight: 120, overflow: "auto",
                color: "var(--color-text-primary)",
              }}>
                {revealed}
              </pre>
            )}
            {!isRevealed && (
              <div style={{ fontFamily: "var(--font-mono)", fontSize: 12, color: "var(--color-text-tertiary)", letterSpacing: 2, paddingLeft: 4 }}>
                {"•".repeat(Math.min(bytes, 16))}
              </div>
            )}
          </div>
        );
      })}
    </InspectorSection>
  );
}

// ageFromUnix formats a Unix timestamp as a human-readable age string.
// Returns "never" when unix is 0.
function ageFromUnix(unix: number): string {
  if (unix === 0) return "never";
  const ms = Date.now() - unix * 1000;
  if (Number.isNaN(ms) || ms < 0) return "";
  const sec = Math.floor(ms / 1000);
  if (sec < 60) return `${sec}s ago`;
  if (sec < 3600) return `${Math.floor(sec / 60)}m ago`;
  if (sec < 86400) return `${Math.floor(sec / 3600)}h ago`;
  return `${Math.floor(sec / 86400)}d ago`;
}

// parsePercent parses an integer percentage string like "70%" → 70.
// Returns null if the string doesn't end in "%".
function parsePercent(s: string): number | null {
  if (!s.endsWith("%")) return null;
  const n = parseInt(s.slice(0, -1), 10);
  return Number.isNaN(n) ? null : n;
}

// HPAScalingSection renders the scaling summary for an autoscaling HPA:
// replica line, scale-target line (with workloads lens link for
// Deployment/StatefulSet), per-metric table, and last-scale age.
function HPAScalingSection({ scaling }: { cluster: string; scaling: HPAScalingDTO }) {
  const atMax = scaling.currentReplicas >= scaling.maxReplicas && scaling.desiredReplicas >= scaling.maxReplicas;
  const replicaMismatch = scaling.desiredReplicas !== scaling.currentReplicas;

  const replicaColor = atMax
    ? "var(--color-text-warning)"
    : replicaMismatch
    ? "var(--color-text-info)"
    : "var(--color-text-primary)";

  // Cross-link: for Deployment and StatefulSet, switch to the workloads lens
  // and expand the matching row using the live store state. The workload key
  // is "<kind>/<namespace>/<name>" — we don't know the namespace from the HPA
  // alone so we toggle by name lookup; `toggleWorkloadExpand` handles the key.
  // Since we don't have the HPA's namespace here (the section is standalone),
  // we navigate to workloads and let the user pick; the link is still more
  // useful than plain text.
  const isWorkloadTarget = scaling.targetKind === "Deployment" || scaling.targetKind === "StatefulSet";

  const handleTargetLink = () => {
    useFleet.getState().setSection("workloads");
  };

  const targetEl = isWorkloadTarget ? (
    <button
      onClick={handleTargetLink}
      data-testid="hpa-target-link"
      style={{
        background: "none", border: "none", padding: 0, cursor: "pointer",
        color: "var(--color-text-info)", fontFamily: "var(--font-mono)", fontSize: 11,
      }}
    >
      {scaling.targetKind}/{scaling.targetName}
    </button>
  ) : (
    <span style={{ fontFamily: "var(--font-mono)", fontSize: 11, color: "var(--color-text-secondary)" }}>
      {scaling.targetKind}/{scaling.targetName}
    </span>
  );

  return (
    <InspectorSection title="Scaling" collapsible>
      {/* Replica line: current → desired (min N / max N) */}
      <div style={{ display: "flex", alignItems: "center", gap: 8, marginBottom: 2 }}>
        <span style={{ color: replicaColor, fontWeight: 500, fontSize: 12 }}>
          {scaling.currentReplicas} → {scaling.desiredReplicas}
        </span>
        <span style={{ color: "var(--color-text-tertiary)", fontSize: 11 }}>
          (min {scaling.minReplicas} / max {scaling.maxReplicas})
        </span>
        {atMax && (
          <span style={{
            fontSize: 10, background: "var(--color-text-warning)", color: "var(--color-background-primary)",
            padding: "1px 5px", borderRadius: 3, fontWeight: 600,
          }}>
            at max
          </span>
        )}
      </div>

      {/* Scale target line */}
      <div style={{ fontSize: 12, color: "var(--color-text-secondary)", marginBottom: 6 }}>
        scales {targetEl}
      </div>

      {/* Metrics table */}
      {scaling.metrics.length > 0 && (
        <div style={{ marginBottom: 6 }}>
          {scaling.metrics.map((m, i) => {
            // Colour current value warning when both current and target are "%"
            // and current exceeds target (the classic saturation signal).
            // We only parse percentages — other quantity formats (e.g. "100m") are
            // left neutral because their ordering semantics differ by metric type.
            const targetPct = parsePercent(m.target);
            const currentPct = parsePercent(m.current);
            const currentOverLimit = targetPct !== null && currentPct !== null && currentPct > targetPct;
            const currentColor = currentOverLimit
              ? "var(--color-text-warning)"
              : "var(--color-text-primary)";
            const currentDisplay = m.current === ""
              ? <span style={{ color: "var(--color-text-tertiary)" }}>—</span>
              : <span style={{ color: currentColor }}>{m.current}</span>;

            return (
              <div key={i} style={{ display: "flex", gap: 8, alignItems: "baseline", fontSize: 11, padding: "2px 0" }}>
                <span style={{ fontFamily: "var(--font-mono)", color: "var(--color-text-primary)", minWidth: 60 }}>{m.name}</span>
                <span style={{ color: "var(--color-text-tertiary)", fontSize: 10 }}>{m.type}</span>
                <span style={{ color: "var(--color-text-secondary)" }}>{currentDisplay} / {m.target}</span>
              </div>
            );
          })}
        </div>
      )}

      {/* Last scale */}
      <div style={{ fontSize: 11, color: "var(--color-text-tertiary)" }}>
        last scaled: {ageFromUnix(scaling.lastScaleUnix)}
      </div>
    </InspectorSection>
  );
}

// ServiceBackingSection renders endpoint health for a v1 Service: a summary
// line (ready/notReady counts), port list, address list (with pod cross-links
// for Pod-targeted endpoints), and selector chips.
function ServiceBackingSection({
  cluster, ns, serviceName, backing,
}: {
  cluster: string;
  ns: string; // the service's namespace - endpoint pods always live in it
  serviceName: string;
  backing: ServiceBackingDTO;
}) {
  const [forwardPort, setForwardPort] = useState<number | null>(null);
  const summaryColor = backing.ready > 0 ? "var(--color-text-success)" : "var(--color-text-danger)";
  const summaryText = backing.ready > 0
    ? `${backing.ready} ready / ${backing.notReady} not ready`
    : `no ready endpoints (${backing.notReady} not ready)`;

  const handlePodLink = (name: string) => {
    useFleet.getState().setSection("pods");
    void openPodDetail(cluster, ns, name);
  };

  return (
    <InspectorSection title="Backing" collapsible>
      {/* Summary line */}
      <div style={{ color: summaryColor, fontWeight: 500, fontSize: 12, marginBottom: 4 }}>
        {summaryText}
      </div>

      {/* Ports row */}
      {backing.ports.length > 0 && (
        <div style={{ display: "flex", gap: 6, flexWrap: "wrap", marginBottom: 4, alignItems: "center" }}>
          {backing.ports.map((p, i) => (
            <span key={i} style={{ display: "inline-flex", alignItems: "center", gap: 4 }}>
              {forwardPort === p.port ? (
                <ForwardPopover
                  cluster={cluster}
                  namespace={ns}
                  kind="Service"
                  name={serviceName}
                  prefillTargetPort={p.port}
                  onClose={() => setForwardPort(null)}
                />
              ) : (
                <button
                  onClick={() => setForwardPort(p.port)}
                  data-testid={`svc-forward-${p.port}`}
                  title={`forward ${p.port}`}
                  style={{ fontSize: 11, background: "var(--color-background-secondary)", padding: "1px 6px", borderRadius: 3, fontFamily: "var(--font-mono)", color: "var(--color-text-secondary)", border: "0.5px solid var(--color-border-tertiary)", cursor: "pointer" }}
                >
                  {p.name ? `${p.name} ` : ""}{p.port}/{p.protocol} ⇄
                </button>
              )}
            </span>
          ))}
        </div>
      )}

      {/* Address list */}
      {backing.addresses.length > 0 && (
        <div style={{ display: "flex", flexDirection: "column", gap: 2, marginBottom: 4 }}>
          {backing.addresses.map((a, i) => {
            const dot = (
              <span style={{ width: 7, height: 7, borderRadius: "50%", display: "inline-block", flexShrink: 0, background: a.ready ? "var(--color-text-success)" : "var(--color-text-danger)" }} />
            );
            const target = a.targetKind === "Pod" ? (
              <button
                onClick={() => handlePodLink(a.targetName)}
                style={{ background: "none", border: "none", padding: 0, cursor: "pointer", color: "var(--color-text-info)", fontFamily: "var(--font-mono)", fontSize: 11, textAlign: "left" }}
                title={`${a.targetKind}/${a.targetName}`}
                data-testid={`pod-link-${a.targetName}`}
              >
                {a.targetName}
              </button>
            ) : a.targetName ? (
              <span style={{ fontFamily: "var(--font-mono)", fontSize: 11, color: "var(--color-text-secondary)" }}>{a.targetName}</span>
            ) : null;
            return (
              <div key={i} style={{ display: "flex", alignItems: "center", gap: 6 }}>
                {dot}
                <span style={{ fontFamily: "var(--font-mono)", fontSize: 11, color: "var(--color-text-primary)" }}>{a.ip}</span>
                {target}
              </div>
            );
          })}
        </div>
      )}

      {/* Selector chips */}
      {backing.selector && Object.keys(backing.selector).length > 0 && (
        <div style={{ display: "flex", gap: 5, flexWrap: "wrap" }}>
          {Object.entries(backing.selector).map(([k, v]) => (
            <span key={k} style={{ fontSize: 10, background: "var(--color-background-secondary)", color: "var(--color-text-tertiary)", padding: "1px 6px", borderRadius: 3, fontFamily: "var(--font-mono)" }}>{k}={v}</span>
          ))}
        </div>
      )}
    </InspectorSection>
  );
}

// InstanceDetail renders as a resizable side panel inside InstanceList
// (master-detail, the Pods pattern) - never as a full-page swap that throws
// the list away. The panel header carries kind + name + refresh + close; the
// drill path lives in the Header breadcrumb and the list beside it.
export function InstanceDetail({ cluster, resource, instance }: { cluster: string; resource: ResourceRef; instance: InstanceRef }) {
  const id = useFleet((s) => s.instanceDetail);
  const closeInstance = useFleet((s) => s.closeInstance);
  const [copied, setCopied] = useState(false);
  const [describing, setDescribing] = useState(false);
  const [tab, setTab] = useState<DetailTab>("summary");

  useEffect(() => {
    setTab("summary");
    void getInstanceDetail(cluster, resource, instance);
    return () => useFleet.getState().clearInstanceDetail();
  }, [cluster, resource.group, resource.version, resource.plural, instance.namespace, instance.name]);

  const isCurrent = id.ref && id.ref.namespace === instance.namespace && id.ref.name === instance.name;
  const d = isCurrent ? id.detail : null;

  // AI assist — on-demand, read-only; two modes (cross-lens explain, and a fix
  // suggestion the operator commits to Git). Reset on selection change.
  const ai = useAIAvailability(cluster, true);
  const [aiPanel, setAiPanel] = useState<{ mode: "explain" | "fix"; loading: boolean; result: AIResultDTO | null } | null>(null);
  // Fence AI results against a mid-flight selection change: the panel component is
  // reused across selections, so a slow explain/fix from the previous resource must
  // not land under the new one. The token bumps on every selection change.
  const aiTokenRef = useRef(0);
  useEffect(() => { aiTokenRef.current++; setAiPanel(null); }, [resource.group, resource.plural, instance.namespace, instance.name]);
  const runAI = (mode: "explain" | "fix") => {
    const token = aiTokenRef.current;
    setAiPanel({ mode, loading: true, result: null });
    const verb = mode === "fix" ? "fix suggestion" : "explain";
    const call = mode === "fix"
      ? suggestFix(cluster, resource.group, resource.version, resource.plural, instance.namespace, instance.name)
      : explainResource(cluster, resource.group, resource.version, resource.plural, instance.namespace, instance.name);
    call
      .then((r) => {
        if (token !== aiTokenRef.current) return; // selection changed mid-flight
        setAiPanel({ mode, loading: false, result: r });
        if (r.error) {
          useFleet.getState().setActionStatus({ kind: "error", message: `AI ${verb} failed: ${r.error}` });
        } else {
          useFleet.getState().setActionStatus({ kind: "success", message: `AI ${verb} — ${resource.kind}/${instance.namespace ? `${instance.namespace}/` : ""}${instance.name} via ${r.backend}` });
        }
      })
      .catch((e) => {
        if (token !== aiTokenRef.current) return;
        setAiPanel({ mode, loading: false, result: null });
        useFleet.getState().setActionStatus({ kind: "error", message: `AI ${verb} failed: ${String(e)}` });
      });
  };
  const aiBusy = aiPanel?.loading ?? false;

  const onCopy = () => {
    if (!d) return;
    void copyText(d.yaml);
    setCopied(true);
    setTimeout(() => setCopied(false), 1500);
  };

  return (
    <div style={{ height: "100%", minHeight: 0, overflow: "hidden", boxSizing: "border-box", display: "flex", flexDirection: "column" }}>
      <div style={{ display: "flex", alignItems: "center", gap: 8, marginBottom: 12, flexShrink: 0, paddingBottom: 6, borderBottom: "0.5px solid var(--color-border-tertiary)" }}>
        <span style={{ fontSize: 10, color: "var(--color-text-tertiary)", flexShrink: 0 }}>{resource.kind}</span>
        <span style={{ fontFamily: "var(--font-mono)", fontSize: 12, fontWeight: 500, color: "var(--color-text-primary)", ...ellipsis }} title={`${instance.namespace ? `${instance.namespace}/` : ""}${instance.name}`}>
          {instance.namespace ? <span style={{ color: "var(--color-text-tertiary)" }}>{instance.namespace}/</span> : null}{instance.name}
        </span>
        <CopyButton text={instance.name} label={`copy name ${instance.name}`} />
        {d && d.created && <span style={{ fontSize: 11, color: "var(--color-text-tertiary)", flexShrink: 0 }}>{age(d.created)}</span>}
        <div style={{ flex: 1 }} />
        {ai.ready && (
          <>
            <button
              onClick={() => runAI("explain")}
              disabled={!ai.egress || aiBusy}
              title={ai.egress ? "Explain the root cause across related objects (AI, read-only)" : "AI is disabled for this protected cluster — use a local backend or set ai.allowProtectedEgress"}
              style={{ ...actionBtn, display: "inline-flex", alignItems: "center", gap: 4, color: "var(--color-text-info)", borderColor: "var(--color-border-info)", opacity: ai.egress ? 1 : 0.5, cursor: ai.egress && !aiBusy ? "pointer" : "not-allowed" }}
            >
              <IconSparkles size={12} stroke={1.5} />
              {aiPanel?.mode === "explain" && aiBusy ? "explaining…" : "Explain"}
            </button>
            <button
              onClick={() => runAI("fix")}
              disabled={!ai.egress || aiBusy}
              title={ai.egress ? "Draft a fix to commit to Git (AI, read-only — Klyx never applies it)" : "AI is disabled for this protected cluster — use a local backend or set ai.allowProtectedEgress"}
              style={{ ...actionBtn, display: "inline-flex", alignItems: "center", gap: 4, color: "var(--color-text-info)", borderColor: "var(--color-border-info)", opacity: ai.egress ? 1 : 0.5, cursor: ai.egress && !aiBusy ? "pointer" : "not-allowed" }}
            >
              <IconSparkles size={12} stroke={1.5} />
              {aiPanel?.mode === "fix" && aiBusy ? "suggesting…" : "Suggest fix"}
            </button>
          </>
        )}
        <button onClick={() => setDescribing(true)} style={actionBtn}>describe</button>
        <button onClick={() => void getInstanceDetail(cluster, resource, instance)} style={actionBtn}>Refresh</button>
        <button onClick={closeInstance} aria-label="close instance panel" style={{ ...actionBtn, display: "inline-flex", alignItems: "center" }}><IconX size={13} stroke={1.5} /></button>
      </div>

      {describing && (
        <DescribePane
          cluster={cluster}
          // kubectl's unambiguous resource argument: plural.group for CRDs and
          // any grouped kind, plural alone for the core "" group.
          kindArg={resource.group ? `${resource.plural}.${resource.group}` : resource.plural}
          namespace={instance.namespace}
          name={instance.name}
          onClose={() => setDescribing(false)}
        />
      )}

      {id.loading && !d ? (
        <div style={inspectorStateBox}>Loading {resource.kind} detail…</div>
      ) : !d ? (
        <div style={inspectorStateBox}>Could not load {instance.namespace ? `${instance.namespace}/` : ""}{instance.name}.</div>
      ) : (
        <div style={{ minHeight: 0, overflow: "hidden", display: "flex", flexDirection: "column", flex: 1 }}>
          {(() => {
            // Verdict parity with the pod/workload panels: derive the headline
            // from the object's own conditions when it has any. No conditions
            // means no verdict - never a fabricated "healthy".
            const primary = ["Ready", "Available", "Established", "Synced", "Accepted", "Programmed", "AbleToScale", "Progressing"]
              .map((t) => d.conditions.find((c) => c.type === t))
              .find(Boolean) ?? d.conditions[0];
            if (!primary) return null;
            const color = conditionColor(primary.status, primary.type);
            const tone = color.includes("success") ? ("success" as const) : color.includes("danger") ? ("danger" as const) : ("info" as const);
            return <InspectorStatus tone={tone} label={`${primary.type} ${primary.status}`} reason={primary.reason || undefined} />;
          })()}
          <div style={{ display: "flex", gap: 6, marginBottom: 12, flexShrink: 0 }}>
            <InspectorTabButton active={tab === "summary"} onClick={() => setTab("summary")}>summary</InspectorTabButton>
            <InspectorTabButton active={tab === "related"} onClick={() => setTab("related")}>related {d.related?.length ?? 0}</InspectorTabButton>
            <InspectorTabButton active={tab === "events"} onClick={() => setTab("events")}>events {d.events.length}</InspectorTabButton>
            <InspectorTabButton active={tab === "yaml"} onClick={() => setTab("yaml")}>yaml</InspectorTabButton>
          </div>

          <div data-testid="instance-detail-scroll" style={{ flex: 1, minHeight: 0, overflowY: "auto", paddingRight: 4 }}>
            {tab === "summary" ? (
              <>
                {aiPanel && (
                  <InspectorSection title={aiPanel.mode === "fix" ? "Suggested fix (AI)" : "Explain (AI)"}>
                    {aiPanel.loading ? (
                      <div style={{ fontSize: 11, color: "var(--color-text-tertiary)" }}>{aiPanel.mode === "fix" ? "drafting a change to commit to Git…" : "tracing the chain across related objects…"}</div>
                    ) : aiPanel.result?.error ? (
                      <div style={{ display: "flex", alignItems: "flex-start", gap: 5, fontSize: 11, color: "var(--color-text-danger)" }}>
                        <IconAlertTriangle size={12} stroke={1.5} style={{ flexShrink: 0, marginTop: 1 }} />
                        <span>{aiPanel.result.error}</span>
                      </div>
                    ) : aiPanel.result ? (
                      <div style={{ display: "flex", flexDirection: "column", gap: 6 }}>
                        {aiPanel.mode === "fix" && (
                          <div style={{ display: "flex", alignItems: "center", gap: 6, padding: "5px 8px", borderRadius: 4, border: "0.5px solid var(--color-border-warning)", background: "var(--color-background-warning)", color: "var(--color-text-warning)", fontSize: 10 }}>
                            <IconAlertTriangle size={12} stroke={1.5} style={{ flexShrink: 0 }} />
                            <span>Review and commit this to Git — Klyx will not apply it. Desired state stays in your repo.</span>
                          </div>
                        )}
                        <div style={{ fontSize: 11, color: "var(--color-text-secondary)", whiteSpace: "pre-wrap", lineHeight: 1.5, fontFamily: aiPanel.mode === "fix" ? "var(--font-mono)" : undefined }}>{aiPanel.result.explanation}</div>
                        <div style={{ display: "flex", alignItems: "center", gap: 8, flexWrap: "wrap" }}>
                          {aiPanel.mode === "fix" && (
                            <button onClick={() => { void copyText(aiPanel.result!.explanation); }} style={{ ...actionBtn, fontSize: 10 }}>copy suggestion</button>
                          )}
                          <div style={{ fontFamily: "var(--font-mono)", fontSize: 9, color: "var(--color-text-tertiary)" }}>Grounded on: {aiPanel.result.groundedOn} · {aiPanel.result.model}</div>
                        </div>
                        {aiPanel.mode !== "fix" && (
                          <div style={{ fontSize: 9, color: "var(--color-text-tertiary)", fontStyle: "italic" }}>AI-generated from resolved health — verify before acting.</div>
                        )}
                      </div>
                    ) : null}
                  </InspectorSection>
                )}
                {/* The data path leads - the salient facts a platform engineer
                    reads first - then metadata, typed sections, and conditions. */}
                <FactsSummary facts={d.facts} />
                <MetadataChips title="Labels" values={d.labels} />
                <MetadataChips title="Annotations" values={d.annotations} defaultCollapsed />

                {d.hpaScaling != null && (
                  <HPAScalingSection cluster={cluster} scaling={d.hpaScaling} />
                )}

                {d.serviceBacking != null && (
                  <ServiceBackingSection cluster={cluster} ns={instance.namespace} serviceName={instance.name} backing={d.serviceBacking} />
                )}

                {d.secretKeys && d.secretKeys.length > 0 && (
                  <SecretDataSection cluster={cluster} ns={instance.namespace} name={instance.name} keys={d.secretKeys} />
                )}

                {d.conditions.length > 0 && (
                  <InspectorSection title="Conditions" collapsible>
                    <InspectorConditions conditions={d.conditions} />
                  </InspectorSection>
                )}
              </>
            ) : tab === "related" ? (
              <RelatedSection cluster={cluster} related={d.related ?? []} />
            ) : tab === "events" ? (
              <EventsPanel events={d.events} />
            ) : (
              <YamlPanel cluster={cluster} resource={resource} instance={instance} detail={d} copied={copied} onCopy={onCopy} />
            )}
          </div>
        </div>
      )}
    </div>
  );
}

function RelatedSection({ cluster, related }: { cluster: string; related: RelatedRefDTO[] }) {
  const openRelated = (ref: RelatedRefDTO) => {
    if (ref.kind === "Pod") {
      useFleet.getState().setSection("pods");
      void openPodDetail(cluster, ref.namespace, ref.name);
      return;
    }
    useFleet.getState().openResource({
      group: ref.group,
      version: ref.version,
      plural: ref.plural,
      kind: ref.kind,
      scope: ref.scope,
    });
    useFleet.getState().openInstance(ref.namespace, ref.name);
  };

  if (related.length === 0) {
    return <div style={inspectorStateBox}>No direct related objects found.</div>;
  }

  const groups = related.reduce<Record<string, RelatedRefDTO[]>>((acc, ref) => {
    const key = ref.relation || "related";
    acc[key] = [...(acc[key] ?? []), ref];
    return acc;
  }, {});

  return (
    <>
      {Object.entries(groups).map(([relation, refs]) => (
        <InspectorSection key={relation} title={relation} count={refs.length} collapsible>
          {refs.map((ref) => (
            <button
              key={`${ref.relation}/${ref.group}/${ref.plural}/${ref.namespace}/${ref.name}`}
              onClick={() => openRelated(ref)}
              style={{
                display: "grid",
                gridTemplateColumns: "120px minmax(0, 1fr)",
                gap: 10,
                alignItems: "center",
                textAlign: "left",
                padding: "5px 8px",
                border: "0.5px solid var(--color-border-tertiary)",
                borderRadius: 4,
                background: "var(--color-background-primary)",
                color: "var(--color-text-secondary)",
                cursor: "pointer",
                fontSize: 11,
              }}
            >
              <span style={{ color: "var(--color-text-tertiary)" }}>{ref.kind}</span>
              <span style={{ fontFamily: "var(--font-mono)", color: "var(--color-text-primary)", ...ellipsis }}>
                {ref.namespace ? `${ref.namespace}/` : ""}{ref.name}
              </span>
            </button>
          ))}
        </InspectorSection>
      ))}
    </>
  );
}

function EventsPanel({ events }: { events: EventDTO[] }) {
  return (
    <InspectorSection title="Events" count={events.length}>
      <InspectorEvents events={events} emptyText="No events for this object." />
    </InspectorSection>
  );
}

function YamlPanel({
  cluster,
  resource,
  instance,
  detail,
  copied,
  onCopy,
}: {
  cluster: string;
  resource: ResourceRef;
  instance: InstanceRef;
  detail: InstanceDetailDTO;
  copied: boolean;
  onCopy: () => void;
}) {
  const yaml = detail?.yaml ?? "";
  const [draft, setDraft] = useState(yaml);
  const [baselineYaml, setBaselineYaml] = useState(yaml);
  const [liveYaml, setLiveYaml] = useState(yaml);
  const [status, setStatus] = useState<ActionResult | null>(null);
  const [busy, setBusy] = useState<"dry-run" | "apply" | null>(null);
  const [confirmApply, setConfirmApply] = useState(false);
  // prd lock: applying YAML on a protected cluster requires the typed cluster
  // name (same arming as ConfirmDialog) and registers a one-shot backend grant.
  const isProtected = useFleet((s) => s.clusters.find((c) => c.name === cluster)?.protected ?? false);
  const [typedConfirm, setTypedConfirm] = useState("");
  const applyArmed = !isProtected || typedConfirm === cluster;
  const [showDiff, setShowDiff] = useState(false);
  const [cursor, setCursor] = useState<CursorPosition>({ line: 1, column: 1 });
  const textareaRef = useRef<HTMLTextAreaElement | null>(null);
  const draftRef = useRef(draft);
  const baselineYamlRef = useRef(baselineYaml);
  const gitOwner = detectGitOwner(detail?.labels ?? {}, detail?.annotations ?? {});
  draftRef.current = draft;
  baselineYamlRef.current = baselineYaml;

  // Authoritative ownership (Flux inventory / Argo resources): catches
  // managed-but-UNLABELED objects the label heuristic above misses, and names
  // the owning unit + its prune state. Label detection stays as the instant
  // fallback while this resolves (or when the check degrades).
  const [ownership, setOwnership] = useState<GitOpsOwnershipDTO | null>(null);
  useEffect(() => {
    let live = true;
    setOwnership(null);
    gitOpsOwnership(cluster, resource, instance).then((o) => { if (live) setOwnership(o); });
    return () => { live = false; };
  }, [cluster, resource.group, resource.plural, instance.namespace, instance.name]);
  const owned = ownership && ownership.ownerKind !== "" ? ownership : null;

  useEffect(() => {
    const hadLocalEdits = draftRef.current !== baselineYamlRef.current;
    setLiveYaml(yaml);
    setConfirmApply(false);
    if (!hadLocalEdits) {
      setDraft(yaml);
      setBaselineYaml(yaml);
      setStatus(null);
      setBusy(null);
      setShowDiff(false);
      setCursor({ line: 1, column: 1 });
    }
  }, [yaml]);

  const dirty = draft !== baselineYaml;
  const diff = buildLineDiff(baselineYaml, draft);
  const validation = validateResourceYAML(draft, resource, instance);
  const baselineValidation = validateResourceYAML(baselineYaml, resource, instance);
  const liveValidation = validateResourceYAML(liveYaml, resource, instance);
  const baselineResourceVersion = baselineValidation.identity.metadataResourceVersion;
  const liveResourceVersion = liveValidation.identity.metadataResourceVersion;
  const versionConflict = dirty && !!baselineResourceVersion && !!liveResourceVersion && baselineResourceVersion !== liveResourceVersion;
  const maskedSecret = resource.kind === "Secret" && draft.includes("<masked>");
  const localBlocking = maskedSecret || validation.blocking || versionConflict;
  const lineCount = Math.max(1, draft.split("\n").length);
  const target = `${instance.namespace ? `${instance.namespace}/` : ""}${instance.name}`;
  const reviewLatest = () => {
    setDraft(liveYaml);
    setBaselineYaml(liveYaml);
    setStatus(null);
    setConfirmApply(false);
    setShowDiff(false);
    setCursor({ line: 1, column: 1 });
  };
  const updateCursorFromTextarea = (el: HTMLTextAreaElement) => {
    setCursor(cursorPositionForOffset(el.value, el.selectionStart ?? 0));
  };
  const jumpToIssue = (issue: ValidationIssue) => {
    if (!issue.line || !textareaRef.current) return;
    const start = lineOffset(draft, issue.line, issue.column ?? 1);
    const end = lineEndOffset(draft, start);
    const el = textareaRef.current;
    el.focus();
    el.setSelectionRange(start, Math.max(start, end));
    setCursor(cursorPositionForOffset(draft, start));
  };

  const runDryRun = async () => {
    if (localBlocking) return;
    setBusy("dry-run");
    setStatus(null);
    setConfirmApply(false);
    const r = await dryRunInstanceYAML(cluster, resource, instance, draft);
    setStatus(r.ok ? { ok: true, error: "Server dry-run passed" } : { ok: false, error: formatKubernetesActionError(r.error) });
    setBusy(null);
  };

  const runApply = async () => {
    if (localBlocking) return;
    const submittedYaml = draft;
    const startResourceVersion = liveResourceVersion ?? baselineResourceVersion;
    setBusy("apply");
    setStatus(null);
    const r = await applyInstanceYAML(cluster, resource, instance, submittedYaml);
    setConfirmApply(false);
    if (!r.ok) {
      setStatus({ ok: false, error: formatKubernetesActionError(r.error) });
      setBusy(null);
      return;
    }
    setStatus({ ok: true, error: "Applied, refreshing live object..." });
    try {
      await getInstanceDetail(cluster, resource, instance);
      const refreshed = useFleet.getState().instanceDetail;
      const sameRef = refreshed.ref?.namespace === instance.namespace && refreshed.ref?.name === instance.name;
      const refreshedYaml = sameRef && refreshed.detail?.yaml ? refreshed.detail.yaml : submittedYaml;
      const refreshedResourceVersion = validateResourceYAML(refreshedYaml, resource, instance).identity.metadataResourceVersion;
      setDraft(refreshedYaml);
      setBaselineYaml(refreshedYaml);
      setLiveYaml(refreshedYaml);
      setStatus({
        ok: true,
        error: refreshedResourceVersion
          ? startResourceVersion && startResourceVersion !== refreshedResourceVersion
            ? `Applied and refreshed live object (resourceVersion ${startResourceVersion} -> ${refreshedResourceVersion})`
            : `Applied and refreshed live object (resourceVersion ${refreshedResourceVersion})`
          : "Applied and refreshed live object",
      });
      setShowDiff(false);
    } catch (err) {
      setStatus({ ok: false, error: err instanceof Error ? `Applied, but refresh failed: ${formatKubernetesActionError(err.message)}` : "Applied, but refresh failed" });
    } finally {
      setBusy(null);
    }
  };

  return (
    <div>
      {owned ? (
        <div style={noticeStyle("warning")}>
          {owned.ownerKind} {owned.ownerNamespace ? `${owned.ownerNamespace}/` : ""}{owned.ownerName} owns this object
          {owned.basis === "flux-inventory" ? " (Flux inventory)" : owned.basis === "argo-resources" ? " (Argo resources)" : ""} —
          your edit will be reverted on the next reconcile{owned.ownerSuspended ? " (currently suspended, so deferred until resumed)" : ""}.
        </div>
      ) : gitOwner ? (
        <div style={noticeStyle("warning")}>
          This object appears {gitOwner.tool}-managed ({gitOwner.detail}). Live edits may be reverted by GitOps or release reconciliation.
        </div>
      ) : null}
      {ownership && ownership.warnings.length > 0 && (
        <div style={noticeStyle("warning")}>
          Ownership check degraded: {ownership.warnings.join("; ")}
        </div>
      )}
      {maskedSecret && (
        <div style={noticeStyle("warning")}>
          Masked Secret YAML cannot be applied because it would replace real values with placeholders.
        </div>
      )}
      {versionConflict && (
        <div style={conflictBand}>
          <span>
            Live object changed since this editor opened
            {baselineResourceVersion && liveResourceVersion ? ` (${baselineResourceVersion} -> ${liveResourceVersion})` : ""}.
            Review the latest YAML before dry-run or apply.
          </span>
          <div style={{ flex: 1 }} />
          <button onClick={reviewLatest} style={btnSm}>Review latest</button>
        </div>
      )}
      {dirty && validation.issues.length > 0 && (
        <ValidationPanel issues={validation.issues} onJump={jumpToIssue} />
      )}
      {status && (
        <div style={noticeStyle(status.ok ? "success" : "danger")}>
          {status.error}
        </div>
      )}
      {confirmApply && (
        <div style={confirmBand}>
          <span>Apply these YAML changes to {target}? <span style={{ fontFamily: "var(--font-mono)" }}>+{diff.added} -{diff.removed}</span></span>
          {isProtected && (
            <input
              aria-label="protected cluster confirmation"
              placeholder={cluster}
              value={typedConfirm}
              onChange={(e) => setTypedConfirm(e.target.value)}
              style={{ padding: "3px 8px", fontFamily: "var(--font-mono)", fontSize: 11, background: "var(--color-background-secondary)", border: "0.5px solid var(--color-border-tertiary)", borderRadius: 4, color: "var(--color-text-primary)", width: 160 }}
            />
          )}
          <div style={{ flex: 1 }} />
          <button onClick={() => { setConfirmApply(false); setTypedConfirm(""); }} style={btnSm}>Cancel</button>
          <button
            onClick={() => {
              // Register the typed confirmation with the backend first - a real
              // apply on a protected cluster consumes a one-shot grant there.
              if (isProtected) {
                void armProtected(cluster, typedConfirm).then((r) => {
                  if (r.ok) void runApply();
                  else setStatus({ ok: false, error: r.error || "could not arm protected action" });
                });
              } else {
                void runApply();
              }
            }}
            disabled={!applyArmed || busy !== null}
            style={btnSm}
          >
            {busy === "apply" ? "Applying" : "Apply changes"}
          </button>
        </div>
      )}
      <div style={editorShell}>
        <div style={editorHeader}>
          <div style={{ display: "flex", alignItems: "center", gap: 8, minWidth: 0 }}>
            <span style={editorTitle}>YAML</span>
            <span style={editorTarget} title={target}>{target}</span>
            <span style={dirty ? dirtyBadge : cleanBadge}>{dirty ? "modified" : "saved"}</span>
          </div>
          <div style={{ display: "flex", alignItems: "center", gap: 6, flexShrink: 0 }}>
            {dirty && <button onClick={() => setShowDiff((v) => !v)} style={editorBtn}>{showDiff ? "Hide diff" : `Diff +${diff.added} -${diff.removed}`}</button>}
            {dirty && <button onClick={reviewLatest} style={editorBtn}>Reset</button>}
            <button onClick={onCopy} style={editorBtn}>{copied ? "Copied" : "Copy"}</button>
            <button
              onClick={() => void runDryRun()}
              disabled={!dirty || localBlocking || busy !== null}
              style={commandBtn(!dirty || localBlocking || busy !== null)}
            >
              {busy === "dry-run" ? "Checking" : "Dry-run"}
            </button>
            <button
              onClick={() => setConfirmApply(true)}
              disabled={!dirty || localBlocking || busy !== null}
              style={commandBtn(!dirty || localBlocking || busy !== null, "danger")}
            >
              Apply
            </button>
          </div>
        </div>
        <textarea
          ref={textareaRef}
          aria-label="resource yaml"
          value={draft}
          onChange={(e) => { setDraft(e.target.value); setStatus(null); setConfirmApply(false); updateCursorFromTextarea(e.target); }}
          onClick={(e) => updateCursorFromTextarea(e.currentTarget)}
          onKeyUp={(e) => updateCursorFromTextarea(e.currentTarget)}
          onSelect={(e) => updateCursorFromTextarea(e.currentTarget)}
          disabled={busy === "apply"}
          spellCheck={false}
          style={editorTextarea}
        />
        {dirty && showDiff && <DiffPanel diff={diff} target={target} />}
        <div style={editorFooter}>
          <span>{lineCount} line{lineCount === 1 ? "" : "s"}</span>
          <span>{draft.length} chars</span>
          <span>Ln {cursor.line}, Col {cursor.column}</span>
          {!dirty && liveResourceVersion && <span>rv {liveResourceVersion}</span>}
          {dirty && <span style={{ color: "var(--color-text-warning)" }}>+{diff.added} -{diff.removed}</span>}
          <span style={{ marginLeft: "auto", color: dirty ? "var(--color-text-warning)" : "var(--color-text-tertiary)" }}>
            {dirty ? busy === "apply" ? "applying and refreshing live object" : versionConflict ? "live object changed; review latest YAML first" : validation.blocking ? "local YAML errors must be fixed first" : "server dry-run required before trust" : "current cluster snapshot"}
          </span>
        </div>
      </div>
    </div>
  );
}

function ValidationPanel({ issues, onJump }: { issues: ValidationIssue[]; onJump: (issue: ValidationIssue) => void }) {
  const blocking = issues.some((issue) => issue.tone === "danger");
  const firstLineIssue = issues.find((issue) => issue.line !== undefined);
  return (
    <div style={validationPanel(blocking ? "danger" : "warning")}>
      <div style={{ display: "flex", alignItems: "center", gap: 8, marginBottom: 4 }}>
        <div style={{ ...validationTitle, marginBottom: 0 }}>{blocking ? "Local YAML errors" : "Local YAML warnings"}</div>
        <div style={{ flex: 1 }} />
        {firstLineIssue && (
          <button onClick={() => onJump(firstLineIssue)} style={validationJumpBtn}>
            Jump to line {firstLineIssue.line}
          </button>
        )}
      </div>
      <div style={{ display: "flex", flexDirection: "column", gap: 3 }}>
        {issues.map((issue, index) => (
          <div key={`${issue.message}/${issue.line ?? ""}/${index}`} style={{ display: "flex", gap: 6, alignItems: "baseline" }}>
            <span style={{ color: `var(--color-text-${issue.tone})`, fontWeight: 700 }}>{issue.tone === "danger" ? "error" : "warn"}</span>
            {issue.line && (
              <span style={{ fontFamily: "var(--font-mono)", color: "var(--color-text-tertiary)" }}>
                {issue.line}:{issue.column ?? 1}
              </span>
            )}
            <span>{issue.message}</span>
          </div>
        ))}
      </div>
    </div>
  );
}

function DiffPanel({ diff, target }: { diff: LineDiff; target: string }) {
  const [expandedFolds, setExpandedFolds] = useState<Set<string>>(() => new Set());
  const [copiedDiff, setCopiedDiff] = useState(false);
  const rows = buildFoldedDiffRows(diff, expandedFolds);
  const toggleFold = (id: string) => {
    setExpandedFolds((current) => {
      const next = new Set(current);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  };
  const onCopyDiff = async () => {
    await copyText(formatDiffPlainText(diff, target));
    setCopiedDiff(true);
    setTimeout(() => setCopiedDiff(false), 1500);
  };

  return (
    <div style={diffPanel} aria-label="yaml diff preview">
      <div style={diffHeader}>
        <span>Review diff</span>
        <button onClick={() => void onCopyDiff()} style={diffHeaderBtn}>{copiedDiff ? "Copied" : "Copy diff"}</button>
        <span style={{ marginLeft: "auto", color: "var(--color-text-warning)" }}>+{diff.added} -{diff.removed}</span>
      </div>
      <div style={diffBody}>
        {rows.map((row, i) => {
          if (row.kind === "fold") {
            const first = row.lines[0];
            const last = row.lines[row.lines.length - 1];
            const range = first && last ? ` lines ${first.newLine ?? first.oldLine}-${last.newLine ?? last.oldLine}` : "";
            return (
              <button
                key={`fold/${row.id}/${i}`}
                onClick={() => toggleFold(row.id)}
                style={diffFoldRow}
              >
                <span style={diffNumber}></span>
                <span style={diffNumber}></span>
                <span style={diffMarker}>...</span>
                <span>{row.hidden} unchanged{range}</span>
              </button>
            );
          }

          const line = row.line;
          return (
            <div key={`${line.type}/${line.oldLine ?? "-"}/${line.newLine ?? "-"}/${i}`} style={diffLineStyle(line.type)}>
              <span style={diffNumber}>{line.oldLine ?? ""}</span>
              <span style={diffNumber}>{line.newLine ?? ""}</span>
              <span style={diffMarker}>{line.type === "added" ? "+" : line.type === "removed" ? "-" : " "}</span>
              <span style={{ whiteSpace: "pre-wrap", wordBreak: "break-word" }}>{line.text || " "}</span>
            </div>
          );
        })}
      </div>
    </div>
  );
}

function noticeStyle(tone: "success" | "warning" | "danger"): React.CSSProperties {
  return {
    display: "flex",
    alignItems: "center",
    minHeight: 26,
    marginBottom: 8,
    padding: "5px 8px",
    border: `0.5px solid var(--color-border-${tone})`,
    borderRadius: 5,
    background: `var(--color-background-${tone})`,
    color: `var(--color-text-${tone})`,
    fontSize: 11,
  };
}

const btnSm: React.CSSProperties = {
  ...actionBtn, padding: "2px 8px", fontSize: 10,
};

const editorShell: React.CSSProperties = {
  border: "0.5px solid var(--color-border-tertiary)",
  borderRadius: "var(--border-radius-md)",
  background: "var(--color-background-primary)",
  overflow: "hidden",
};

const editorHeader: React.CSSProperties = {
  minHeight: 36,
  display: "flex",
  alignItems: "center",
  gap: 10,
  padding: "6px 8px",
  borderBottom: "0.5px solid var(--color-border-tertiary)",
  background: "var(--color-background-tertiary)",
};

const editorTitle: React.CSSProperties = {
  fontSize: 10,
  fontWeight: 700,
  textTransform: "uppercase",
  letterSpacing: 0.5,
  color: "var(--color-text-tertiary)",
};

const editorTarget: React.CSSProperties = {
  minWidth: 0,
  maxWidth: 260,
  overflow: "hidden",
  textOverflow: "ellipsis",
  whiteSpace: "nowrap",
  fontFamily: "var(--font-mono)",
  fontSize: 11,
  color: "var(--color-text-secondary)",
};

const cleanBadge: React.CSSProperties = {
  flexShrink: 0,
  padding: "1px 6px",
  borderRadius: 999,
  border: "0.5px solid var(--color-border-tertiary)",
  background: "var(--color-background-secondary)",
  color: "var(--color-text-tertiary)",
  fontSize: 10,
};

const dirtyBadge: React.CSSProperties = {
  ...cleanBadge,
  border: "0.5px solid var(--color-border-warning)",
  background: "var(--color-background-warning)",
  color: "var(--color-text-warning)",
};

const editorBtn: React.CSSProperties = {
  ...btnSm,
  background: "var(--color-background-primary)",
  color: "var(--color-text-secondary)",
};

function commandBtn(disabled: boolean, tone: "neutral" | "danger" = "neutral"): React.CSSProperties {
  return {
    ...editorBtn,
    color: tone === "danger" ? "var(--color-text-danger)" : "var(--color-text-primary)",
    borderColor: tone === "danger" ? "var(--color-border-danger)" : "var(--color-border-tertiary)",
    opacity: disabled ? 0.45 : 1,
    cursor: disabled ? "default" : "pointer",
  };
}

const editorTextarea: React.CSSProperties = {
  display: "block",
  width: "100%",
  minHeight: 390,
  maxHeight: "calc(100vh - 280px)",
  resize: "vertical",
  boxSizing: "border-box",
  margin: 0,
  padding: "12px 14px",
  background: "var(--color-background-secondary)",
  border: 0,
  borderRadius: 0,
  outline: "none",
  fontFamily: "var(--font-mono)",
  fontSize: 12,
  lineHeight: 1.55,
  tabSize: 2,
  overflow: "auto",
  color: "var(--color-text-primary)",
};

const editorFooter: React.CSSProperties = {
  display: "flex",
  alignItems: "center",
  gap: 12,
  minHeight: 28,
  padding: "5px 8px",
  borderTop: "0.5px solid var(--color-border-tertiary)",
  background: "var(--color-background-tertiary)",
  color: "var(--color-text-tertiary)",
  fontFamily: "var(--font-mono)",
  fontSize: 10,
};

function validationPanel(tone: "warning" | "danger"): React.CSSProperties {
  return {
    marginBottom: 8,
    padding: "7px 8px",
    border: `0.5px solid var(--color-border-${tone})`,
    borderRadius: 5,
    background: `var(--color-background-${tone})`,
    color: `var(--color-text-${tone})`,
    fontSize: 11,
  };
}

const validationTitle: React.CSSProperties = {
  marginBottom: 4,
  color: "var(--color-text-primary)",
  fontSize: 10,
  fontWeight: 700,
  textTransform: "uppercase",
  letterSpacing: 0.4,
};

const validationJumpBtn: React.CSSProperties = {
  ...btnSm,
  borderColor: "currentColor",
  background: "transparent",
  color: "inherit",
};

const diffPanel: React.CSSProperties = {
  borderTop: "0.5px solid var(--color-border-tertiary)",
  background: "var(--color-background-primary)",
};

const diffHeader: React.CSSProperties = {
  display: "flex",
  alignItems: "center",
  gap: 8,
  minHeight: 28,
  padding: "5px 8px",
  borderBottom: "0.5px solid var(--color-border-tertiary)",
  background: "var(--color-background-tertiary)",
  color: "var(--color-text-tertiary)",
  fontSize: 10,
  textTransform: "uppercase",
  letterSpacing: 0.4,
};

const diffHeaderBtn: React.CSSProperties = {
  ...btnSm,
  padding: "1px 7px",
  background: "var(--color-background-primary)",
  color: "var(--color-text-secondary)",
  textTransform: "none",
  letterSpacing: 0,
};

const diffBody: React.CSSProperties = {
  maxHeight: 220,
  overflow: "auto",
  fontFamily: "var(--font-mono)",
  fontSize: 11,
  lineHeight: 1.45,
};

function diffLineStyle(type: DiffLine["type"]): React.CSSProperties {
  return {
    display: "grid",
    gridTemplateColumns: "36px 36px 16px minmax(0, 1fr)",
    gap: 6,
    padding: "1px 8px",
    color: type === "added" ? "var(--color-text-success)" : type === "removed" ? "var(--color-text-danger)" : "var(--color-text-secondary)",
    background: type === "added" ? "var(--color-background-success)" : type === "removed" ? "var(--color-background-danger)" : "transparent",
  };
}

const diffFoldRow: React.CSSProperties = {
  ...diffLineStyle("context"),
  width: "100%",
  border: 0,
  borderTop: "0.5px solid var(--color-border-tertiary)",
  borderBottom: "0.5px solid var(--color-border-tertiary)",
  background: "var(--color-background-tertiary)",
  color: "var(--color-text-tertiary)",
  cursor: "pointer",
  fontFamily: "var(--font-mono)",
  fontSize: 11,
  textAlign: "left",
};

const diffNumber: React.CSSProperties = {
  color: "var(--color-text-tertiary)",
  textAlign: "right",
  userSelect: "none",
};

const diffMarker: React.CSSProperties = {
  color: "inherit",
  userSelect: "none",
};

const confirmBand: React.CSSProperties = {
  display: "flex",
  alignItems: "center",
  gap: 8,
  marginBottom: 8,
  padding: "7px 8px",
  border: "0.5px solid var(--color-border-warning)",
  borderRadius: 5,
  background: "var(--color-background-warning)",
  color: "var(--color-text-warning)",
  fontSize: 11,
};

const conflictBand: React.CSSProperties = {
  ...confirmBand,
  border: "0.5px solid var(--color-border-danger)",
  background: "var(--color-background-danger)",
  color: "var(--color-text-danger)",
};

