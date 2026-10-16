import { useEffect, useState } from "react";
import { Rail, type RailStep } from "../chrome/primitives";
import { InspectorBlock } from "../layout/InspectorLayout";
import { dependencyGraph, type DepGraph } from "../bridge/depGraph";
import { notHereYet, type NotHereYet, type NotHereYetResult } from "../bridge/notHereYet";
import { depTree, notHereText, type DepStep } from "../line/depGraph";

// The Flux inspector's Dependencies (The Line slice 8c, parent §5.1): the
// selected unit's source and dependsOn, walked in Go and drawn on Rail.
// Fetched on selection, cancelled when the selection changes (the Go walk
// stops with it), and applied only for the selection that asked. A refresh
// re-walks; nothing polls.

type Walk = { key: string; graph?: DepGraph; error?: string };

const clock = (iso: string) => new Date(iso).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", hour12: false });

export function DependencyGraphSection({ cluster, kind, namespace, name, uid }: { cluster: string; kind: string; namespace: string; name: string; uid: string }) {
  const [tick, setTick] = useState(0);
  const key = `${cluster}\u0000${kind}\u0000${namespace}\u0000${name}\u0000${uid}\u0000${tick}`;
  const [walk, setWalk] = useState<Walk>({ key: "" });
  const [git, setGit] = useState<{ key: string; res?: NotHereYet; error?: string } | null>(null);

  useEffect(() => {
    let live = true;
    const graph = dependencyGraph(cluster, kind, namespace, name, uid);
    let gitCall: ReturnType<typeof notHereYet> | null = null;
    graph.then(
      (g) => {
        if (!live) return;
        setWalk({ key, graph: g });
        const nodes = g.nodes.filter((n) => n.git).map((n) => ({ id: n.id, ...n.git! }));
        if (nodes.length === 0) return;
        gitCall = notHereYet(nodes);
        gitCall.then(
          (res) => live && setGit({ key, res }),
          (e: unknown) => live && setGit({ key, error: e instanceof Error ? e.message : String(e) }),
        );
      },
      (e: unknown) => live && setWalk({ key, error: e instanceof Error ? e.message : String(e) }),
    );
    return () => {
      live = false;
      graph.cancel();
      gitCall?.cancel();
    };
  }, [cluster, kind, namespace, name, uid, key]);

  const mine = walk.key === key ? walk : null;
  const g = mine?.graph;
  const results = new Map<string, NotHereYetResult>();
  const gitNow = git && git.key === key ? git : null;
  if (gitNow?.res) for (const r of gitNow.res.results) results.set(r.id, r);
  const target = gitNow?.res?.target;
  const paths = new Map((g?.nodes ?? []).filter((n) => n.git).map((n) => [n.id, n.git!.path]));
  const err = mine?.error ?? g?.error;

  return (
    <InspectorBlock title="Dependencies" count={g && !g.error ? g.nodes.filter((n) => !n.id.startsWith("stub:")).length : undefined}>
      <div data-testid="dep-graph" style={{ display: "flex", flexDirection: "column", gap: 6 }}>
        <div className="t-secondary" style={{ display: "flex", gap: 8, alignItems: "baseline", flexWrap: "wrap" }}>
          {g && !g.error && <span>{g.reads} read{g.reads === 1 ? "" : "s"}</span>}
          {target && (
            <span data-git-target>
              T {target.name}
              {target.fetchedAt && (target.error
                ? `, history as of ${clock(target.fetchedAt)}; fetch failed: ${target.error}`
                : `, fetched ${clock(target.fetchedAt)}`)}
            </span>
          )}
          {gitNow?.error && <span data-git-error style={{ color: "var(--line-caution, var(--color-text-warning))" }}>not here yet: unavailable (Git read failed: {gitNow.error})</span>}
          <button type="button" className="t-secondary" onClick={() => setTick((t) => t + 1)} style={{ background: "none", border: 0, padding: 0, cursor: "pointer", color: "var(--color-text-info)" }}>
            Re-walk
          </button>
        </div>
        {g && g.incomplete.length > 0 && (
          <div data-testid="dep-incomplete" className="t-body" style={{ color: "var(--line-caution, var(--color-text-warning))", overflowWrap: "anywhere" }}>
            incomplete: {g.incomplete.join("; ")}
          </div>
        )}
        {err ? <div role="alert" className="t-body" style={{ color: "var(--color-text-danger)", overflowWrap: "anywhere" }}>Dependency graph read failed: {err}</div>
          : !g ? <span className="t-secondary">Walking dependencies…</span>
          : <Rail label="Dependencies" steps={depTree(g).map((s) => railStep(s, results, paths))} />}
      </div>
    </InspectorBlock>
  );
}

function railStep(s: DepStep, git: Map<string, NotHereYetResult>, paths: Map<string, string>): RailStep {
  const r = git.get(s.id);
  const lines: { text: string; tone?: "danger" | "muted" }[] = [];
  if (!s.ref) {
    if (s.evidence.condition) lines.push({ text: s.evidence.condition, tone: "danger" });
    for (const x of s.evidence.supply) lines.push({ text: x });
    for (const x of s.evidence.proximity) lines.push({ text: x });
    if (s.evidence.inventory) lines.push({ text: s.evidence.inventory });
    for (const w of s.evidence.writes) lines.push({ text: w, tone: "muted" });
  }
  const gitText = s.ref ? undefined : notHereText(r, paths.get(s.id) ?? "");
  return {
    key: s.key,
    segment: s.segment,
    segmentWords: s.segmentWords,
    name: s.name,
    label: <span className="t-identifier" style={{ fontSize: 11 }}>{s.name}</span>,
    aspect: s.aspect,
    tags: s.tags.length ? s.tags : undefined,
    body: lines.length || gitText ? (
      <div data-evidence={s.chainEnd || undefined} style={{ display: "flex", flexDirection: "column", gap: 2, fontSize: 11 }}>
        {lines.map((l, i) => (
          // a node's lines read in ink-2 (the mockup's chain): its mark and its
          // reported-blocker segment carry the danger (slice 13c.6)
          <div key={i} data-tone={l.tone} style={{ overflowWrap: "anywhere", color: l.tone === "muted" ? "var(--color-text-tertiary)" : "var(--color-text-secondary)" }}>{l.text}</div>
        ))}
        {gitText && <div data-git style={{ color: "var(--color-text-secondary)" }}>{gitText}</div>}
        {r?.state === "behind" && r.commits.length > 0 && (
          <ul style={{ margin: 0, paddingLeft: 14 }}>
            {r.commits.map((c) => <li key={c.sha}><span className="t-identifier">{c.sha.slice(0, 7)}</span> {c.subject}</li>)}
            {r.more && <li className="t-secondary">and more</li>}
          </ul>
        )}
        {r?.state === "behind" && r.files.length > 0 && (
          <ul style={{ margin: 0, paddingLeft: 14 }} aria-label="changed files">
            {r.files.map((f) => <li key={f.path}><span className="t-identifier">{f.status}</span> {f.path}</li>)}
            {r.filesOmitted > 0 && <li className="t-secondary">and {r.filesOmitted} more files</li>}
          </ul>
        )}
      </div>
    ) : undefined,
    branches: s.branches.length ? s.branches.map((b) => b.map((x) => railStep(x, git, paths))) : undefined,
  };
}
