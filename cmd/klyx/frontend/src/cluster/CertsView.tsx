import { useEffect, useMemo, useState } from "react";
import type { CSSProperties, ReactNode } from "react";
import { useFleet } from "../store/fleet";
import type { CertificateDTO, IssuerDTO } from "../store/fleet";
import { openCertManager, closeCertManager } from "../bridge/certmanager";

const readyColor: Record<string, string> = {
  Ready: "var(--color-text-success)",
  Issuing: "var(--color-text-info)",
  Failed: "var(--color-text-danger)",
  Unknown: "var(--color-text-tertiary)",
};
const ellipsis: CSSProperties = { overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" };
const frame: CSSProperties = {
  border: "0.5px solid var(--color-border-tertiary)",
  borderRadius: "var(--border-radius-md)",
  background: "var(--color-background-primary)",
};
const mono: CSSProperties = { fontFamily: "var(--font-mono)" };

type CertFilter = "all" | "attention" | "expiring" | "issuers";

const keyOf = (c: { namespace: string; name: string }) => `${c.namespace}/${c.name}`;

const nowSec = () => Math.floor(Date.now() / 1000);

function expired(c: CertificateDTO): boolean {
  return c.notAfterUnix > 0 && c.notAfterUnix <= nowSec();
}

// expiringSoon: cert-manager's renewalTime already encodes renewBefore, so
// now >= renewalTime is the truthful "in the renewal window" signal.
function expiringSoon(c: CertificateDTO): boolean {
  return !expired(c) && c.renewalUnix > 0 && nowSec() >= c.renewalUnix;
}

function expiryText(c: CertificateDTO): string {
  if (c.notAfterUnix === 0) return "—";
  const secs = c.notAfterUnix - nowSec();
  if (secs <= 0) return "expired";
  if (secs < 3600) return `${Math.floor(secs / 60)}m`;
  if (secs < 86400) return `${Math.floor(secs / 3600)}h`;
  return `${Math.floor(secs / 86400)}d`;
}

// A Ready cert that is merely in the renewal window is NOT attention (cert-manager
// renews it automatically); an expired or not-Ready cert is.
function needsAttention(c: CertificateDTO): boolean {
  return c.ready !== "Ready" || expired(c);
}

function certRank(c: CertificateDTO): number {
  if (c.ready === "Failed" || expired(c)) return 0;
  if (c.ready === "Issuing") return 1;
  if (expiringSoon(c)) return 2;
  if (c.ready !== "Ready") return 3;
  return 4;
}

function issuerRank(i: IssuerDTO): number {
  if (i.ready === "Failed") return 0;
  if (i.ready !== "Ready") return 1;
  return 2;
}

function statusText(c: CertificateDTO): string {
  if (expired(c)) return "expired";
  return c.ready.toLowerCase();
}

// resolveIssuer finds the bound Issuer/ClusterIssuer for a certificate. A
// ClusterIssuer is cluster-scoped; an Issuer is matched within the cert's namespace.
function resolveIssuer(c: CertificateDTO, issuers: IssuerDTO[]): IssuerDTO | undefined {
  return issuers.find(
    (i) => i.kind === c.issuerKind && i.name === c.issuerName && (i.kind === "ClusterIssuer" || i.namespace === c.namespace),
  );
}

function matchesFilter(c: CertificateDTO, filter: CertFilter): boolean {
  if (filter === "attention") return needsAttention(c);
  if (filter === "expiring") return expiringSoon(c) || expired(c);
  return true;
}

function matchesQuery(c: CertificateDTO, query: string): boolean {
  const q = query.trim().toLowerCase();
  if (!q) return true;
  return [c.namespace, c.name, c.issuerName, c.secretName, c.reason, ...c.dnsNames].some((v) => v.toLowerCase().includes(q));
}

export function CertsView({ cluster }: { cluster: string }) {
  const tier = useFleet((s) => s.clusters.find((c) => c.name === cluster)?.certManagerTier ?? "Unknown");
  const cm = useFleet((s) => s.certmanager);
  const select = useFleet((s) => s.selectCertificate);
  const [filter, setFilter] = useState<CertFilter>("all");
  const [query, setQuery] = useState("");
  const absent = tier === "Absent";

  useEffect(() => {
    if (absent) return;
    let off = () => {};
    openCertManager(cluster).then((u) => (off = u)).catch((e) => console.error("openCertManager", e));
    return () => {
      off();
      void closeCertManager(cluster);
    };
  }, [cluster, absent]);

  const certs = cm.cluster === cluster ? cm.certificates : [];
  const issuers = cm.cluster === cluster ? cm.issuers : [];
  const orderedCerts = useMemo(
    () => [...certs].sort((a, b) => certRank(a) - certRank(b) || a.namespace.localeCompare(b.namespace) || a.name.localeCompare(b.name)),
    [certs],
  );
  const visibleCerts = useMemo(
    () => orderedCerts.filter((c) => matchesFilter(c, filter) && matchesQuery(c, query)),
    [orderedCerts, filter, query],
  );
  const orderedIssuers = useMemo(
    () => [...issuers].sort((a, b) => issuerRank(a) - issuerRank(b) || a.kind.localeCompare(b.kind) || a.name.localeCompare(b.name)),
    [issuers],
  );

  if (absent) {
    return <div style={{ padding: 24, color: "var(--color-text-secondary)", fontSize: 13 }}>cert-manager not detected on this cluster.</div>;
  }

  const attention = certs.filter(needsAttention).length;
  const issuing = certs.filter((c) => c.ready === "Issuing").length;
  const expiring = certs.filter((c) => expiringSoon(c) || expired(c)).length;
  const ready = certs.filter((c) => c.ready === "Ready" && !expired(c)).length;
  const selected = visibleCerts.find((c) => keyOf(c) === cm.selectedKey) ?? null;

  return (
    <div style={{ padding: "14px 16px", display: "flex", flexDirection: "column", gap: 12, height: "100%", minHeight: 0, overflow: "hidden", boxSizing: "border-box" }}>
      <div style={{ display: "flex", alignItems: "flex-start", justifyContent: "space-between", gap: 16 }}>
        <div>
          <div style={{ fontSize: 10, color: "var(--color-text-tertiary)", marginBottom: 3 }}>cert-manager</div>
          <div style={{ fontSize: 18, fontWeight: 600 }}>Certificates</div>
        </div>
        <Badge tone={attention > 0 ? "warning" : "success"}>{attention > 0 ? `${attention} need attention` : "ready"}</Badge>
      </div>

      <div style={{ ...frame, display: "grid", gridTemplateColumns: "repeat(auto-fit, minmax(130px, 1fr))", overflow: "hidden" }}>
        <SummaryCell label="health" value={attention > 0 ? `${attention} attention` : "ready"} tone={attention > 0 ? "warning" : "success"} />
        <SummaryCell label="certificates" value={String(certs.length)} />
        <SummaryCell label="issuing" value={String(issuing)} tone={issuing > 0 ? "info" : "muted"} />
        <SummaryCell label="expiring" value={String(expiring)} tone={expiring > 0 ? "warning" : "muted"} />
        <SummaryCell label="issuers" value={String(issuers.length)} />
        <SummaryCell label="ready" value={String(ready)} tone="success" />
      </div>

      <div style={{ display: "flex", flexWrap: "wrap", gap: 6, alignItems: "center" }}>
        <FilterButton label="all" active={filter === "all"} count={certs.length} onClick={() => setFilter("all")} />
        <FilterButton label="needs attention" active={filter === "attention"} count={attention} onClick={() => setFilter("attention")} />
        <FilterButton label="expiring" active={filter === "expiring"} count={expiring} onClick={() => setFilter("expiring")} />
        <FilterButton label="issuers" active={filter === "issuers"} count={issuers.length} onClick={() => setFilter("issuers")} />
        <input
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder="filter certificates"
          aria-label="filter certificates"
          style={{ marginLeft: "auto", fontSize: 11, padding: "3px 8px", borderRadius: 4, border: "0.5px solid var(--color-border-tertiary)", background: "var(--color-background-primary)", color: "var(--color-text-primary)", width: 180 }}
        />
        {visibleCerts.length !== certs.length && filter !== "issuers" && <span style={{ fontSize: 10, color: "var(--color-text-tertiary)" }}>{visibleCerts.length} of {certs.length}</span>}
      </div>

      {cm.loading && certs.length === 0 && issuers.length === 0 ? (
        <div style={{ color: "var(--color-text-secondary)", fontSize: 13 }}>Loading certificate state…</div>
      ) : filter === "issuers" ? (
        <IssuersPanel issuers={orderedIssuers} />
      ) : certs.length === 0 ? (
        <div style={{ color: "var(--color-text-secondary)", fontSize: 13 }}>No certificates found.</div>
      ) : (
        <div style={{ flex: 1, minHeight: 0, display: "grid", gridTemplateColumns: "minmax(440px, 1.25fr) minmax(320px, 0.75fr)", gap: 12, alignItems: "stretch", overflow: "hidden" }}>
          <div style={{ ...frame, overflow: "hidden", minHeight: 0, display: "flex", flexDirection: "column" }}>
            <div style={{ display: "grid", gridTemplateColumns: "3px minmax(180px,1.3fr) minmax(120px,1fr) 96px 64px 84px", gap: 10, alignItems: "center", padding: "5px 12px", borderBottom: "0.5px solid var(--color-border-tertiary)", fontSize: 10, color: "var(--color-text-tertiary)", flexShrink: 0 }}>
              <span />
              <span>certificate</span>
              <span>issuer</span>
              <span>secret</span>
              <span>expiry</span>
              <span>status</span>
            </div>
            <div data-testid="cert-scroll" style={{ flex: 1, minHeight: 0, overflowY: "auto" }}>
              {visibleCerts.length === 0 ? (
                <div style={{ padding: 14, color: "var(--color-text-secondary)", fontSize: 13 }}>No certificates match the current filter.</div>
              ) : visibleCerts.map((c) => {
                const k = keyOf(c);
                return <CertRow key={k} c={c} open={cm.selectedKey === k} onClick={() => (cm.selectedKey === k ? select(null) : select(k))} />;
              })}
            </div>
          </div>

          <div data-testid="cert-inspector" style={{ minHeight: 0, overflowY: "auto" }}>
            {selected ? <CertInspector c={selected} issuer={resolveIssuer(selected, issuers)} /> : <EmptyInspector attention={attention} />}
          </div>
        </div>
      )}
    </div>
  );
}

function CertRow({ c, open, onClick }: { c: CertificateDTO; open: boolean; onClick: () => void }) {
  const bad = c.ready === "Failed" || expired(c);
  const soon = expiringSoon(c);
  return (
    <button
      onClick={onClick}
      style={{
        width: "100%", textAlign: "left", display: "grid",
        gridTemplateColumns: "3px minmax(180px,1.3fr) minmax(120px,1fr) 96px 64px 84px",
        gap: 10, alignItems: "center", padding: "7px 12px", cursor: "pointer",
        border: "none", borderBottom: "0.5px solid var(--color-border-tertiary)",
        background: open ? "var(--color-background-secondary)" : "transparent", color: "var(--color-text-primary)",
      }}
    >
      <span style={{ alignSelf: "stretch", background: bad ? "var(--color-text-danger)" : c.ready === "Issuing" ? "var(--color-text-info)" : soon ? "var(--color-text-warning)" : "transparent" }} />
      <span style={{ ...mono, ...ellipsis, fontSize: 12 }} title={`${c.namespace}/${c.name}`}>
        <span style={{ color: "var(--color-text-tertiary)" }}>{c.namespace}/</span>{c.name}
      </span>
      <span style={{ ...mono, ...ellipsis, fontSize: 11, color: "var(--color-text-secondary)" }} title={`${c.issuerKind}/${c.issuerName}`}>{c.issuerName}</span>
      <span style={{ ...mono, ...ellipsis, fontSize: 11, color: "var(--color-text-secondary)" }} title={c.secretName}>{c.secretName}</span>
      <span style={{ ...mono, fontSize: 11, color: expired(c) ? "var(--color-text-danger)" : soon ? "var(--color-text-warning)" : "var(--color-text-secondary)" }}>{expiryText(c)}</span>
      <span style={{ display: "flex", alignItems: "center", gap: 5, minWidth: 0 }}>
        <span style={{ width: 6, height: 6, borderRadius: "50%", background: readyColor[c.ready] ?? readyColor.Unknown, flexShrink: 0 }} />
        <span style={{ ...ellipsis, fontSize: 11, color: "var(--color-text-secondary)" }}>{statusText(c)}</span>
      </span>
    </button>
  );
}

function CertInspector({ c, issuer }: { c: CertificateDTO; issuer: IssuerDTO | undefined }) {
  const issuerBroken = issuer && issuer.ready !== "Ready";
  return (
    <div style={{ ...frame, padding: 14, display: "flex", flexDirection: "column", gap: 12 }}>
      <div>
        <div style={{ ...mono, fontSize: 13, fontWeight: 500 }}>{c.namespace}/{c.name}</div>
        <div style={{ display: "flex", alignItems: "center", gap: 6, marginTop: 4 }}>
          <span style={{ width: 7, height: 7, borderRadius: "50%", background: readyColor[c.ready] ?? readyColor.Unknown }} />
          <span style={{ fontSize: 12, color: "var(--color-text-secondary)" }}>{statusText(c)}</span>
          {c.reason && <span style={{ ...mono, fontSize: 10, padding: "1px 6px", borderRadius: 4, background: "var(--color-background-secondary)", color: c.ready === "Failed" ? "var(--color-text-danger)" : "var(--color-text-secondary)" }}>{c.reason}</span>}
        </div>
      </div>

      {/* A failing bound issuer is the headline for a stuck cert (M10 source pattern). */}
      {issuerBroken && (
        <div style={{ border: "0.5px solid var(--color-text-danger)", borderRadius: "var(--border-radius-md)", padding: "8px 10px", background: "var(--color-background-secondary)" }}>
          <div style={{ fontSize: 11, color: "var(--color-text-danger)", fontWeight: 500 }}>issuer not ready: {issuer!.reason || "unknown"}</div>
          <div style={{ ...mono, fontSize: 11, color: "var(--color-text-secondary)", marginTop: 2 }}>{issuer!.kind}/{issuer!.name}</div>
          {issuer!.message && <div style={{ fontSize: 11, color: "var(--color-text-secondary)", marginTop: 3 }}>{issuer!.message}</div>}
        </div>
      )}

      <Section title="issuer">
        <Field label="ref" value={`${c.issuerKind}/${c.issuerName}`} />
        <Field label="health" value={issuer ? issuer.ready : "not found"} tone={issuer ? (issuer.ready === "Ready" ? "success" : "danger") : "muted"} />
      </Section>

      <Section title="secret">
        <Field label="name" value={c.secretName || "—"} />
      </Section>

      <Section title="validity">
        <Field label="expiry" value={c.notAfterUnix ? `${expiryText(c)} (${new Date(c.notAfterUnix * 1000).toISOString().slice(0, 10)})` : "not issued"} tone={expired(c) ? "danger" : expiringSoon(c) ? "warning" : "muted"} />
        <Field label="renews" value={c.renewalUnix ? new Date(c.renewalUnix * 1000).toISOString().slice(0, 10) : "—"} />
      </Section>

      {c.dnsNames.length > 0 && (
        <Section title="dns names">
          {c.dnsNames.map((d) => <div key={d} style={{ ...mono, fontSize: 11, color: "var(--color-text-secondary)" }}>{d}</div>)}
        </Section>
      )}

      {c.message && (
        <Section title="message">
          <div style={{ fontSize: 11, color: "var(--color-text-secondary)" }}>{c.message}</div>
        </Section>
      )}
    </div>
  );
}

function IssuersPanel({ issuers }: { issuers: IssuerDTO[] }) {
  if (issuers.length === 0) {
    return <div style={{ color: "var(--color-text-secondary)", fontSize: 13 }}>No issuers found.</div>;
  }
  return (
    <div style={{ ...frame, overflow: "hidden", minHeight: 0, flex: 1, display: "flex", flexDirection: "column" }}>
      <div data-testid="issuer-scroll" style={{ flex: 1, minHeight: 0, overflowY: "auto" }}>
        {issuers.map((i) => (
          <div key={`${i.kind}/${i.namespace}/${i.name}`} style={{ display: "grid", gridTemplateColumns: "3px minmax(180px,1.4fr) 110px minmax(120px,1fr)", gap: 10, alignItems: "center", padding: "7px 12px", borderBottom: "0.5px solid var(--color-border-tertiary)" }}>
            <span style={{ alignSelf: "stretch", background: i.ready === "Failed" ? "var(--color-text-danger)" : i.ready !== "Ready" ? "var(--color-text-warning)" : "transparent" }} />
            <span style={{ ...mono, ...ellipsis, fontSize: 12 }} title={`${i.kind}/${i.namespace}/${i.name}`}>
              {i.namespace && <span style={{ color: "var(--color-text-tertiary)" }}>{i.namespace}/</span>}{i.name}
            </span>
            <span style={{ fontSize: 10, color: "var(--color-text-tertiary)" }}>{i.kind}</span>
            <span style={{ display: "flex", alignItems: "center", gap: 5 }}>
              <span style={{ width: 6, height: 6, borderRadius: "50%", background: readyColor[i.ready] ?? readyColor.Unknown }} />
              <span style={{ ...ellipsis, fontSize: 11, color: i.ready === "Failed" ? "var(--color-text-danger)" : "var(--color-text-secondary)" }} title={i.message}>{i.ready !== "Ready" && i.reason ? i.reason : i.ready.toLowerCase()}</span>
            </span>
          </div>
        ))}
      </div>
    </div>
  );
}

function EmptyInspector({ attention }: { attention: number }) {
  return (
    <div style={{ ...frame, padding: 18, color: "var(--color-text-secondary)", fontSize: 12, height: "100%", boxSizing: "border-box" }}>
      {attention > 0 ? "Select a certificate to inspect its issuer and validity." : "All certificates are ready. Select one to inspect it."}
    </div>
  );
}

type Tone = "success" | "warning" | "danger" | "info" | "muted";
const toneColor: Record<Tone, string> = {
  success: "var(--color-text-success)",
  warning: "var(--color-text-warning)",
  danger: "var(--color-text-danger)",
  info: "var(--color-text-info)",
  muted: "var(--color-text-tertiary)",
};

function Badge({ tone, children }: { tone: Tone; children: ReactNode }) {
  return <span style={{ fontSize: 11, padding: "2px 8px", borderRadius: 4, border: `0.5px solid ${toneColor[tone]}`, color: toneColor[tone] }}>{children}</span>;
}

function SummaryCell({ label, value, tone = "muted" }: { label: string; value: string; tone?: Tone }) {
  return (
    <div style={{ padding: "8px 12px", borderRight: "0.5px solid var(--color-border-tertiary)" }}>
      <div style={{ fontSize: 10, color: "var(--color-text-tertiary)" }}>{label}</div>
      <div style={{ fontSize: 15, fontWeight: 500, color: tone === "muted" ? "var(--color-text-primary)" : toneColor[tone] }}>{value}</div>
    </div>
  );
}

function FilterButton({ label, active, count, onClick }: { label: string; active: boolean; count: number; onClick: () => void }) {
  return (
    <button
      onClick={onClick}
      style={{
        fontSize: 11, padding: "3px 9px", borderRadius: 4, cursor: "pointer",
        border: `0.5px solid ${active ? "var(--color-text-info)" : "var(--color-border-tertiary)"}`,
        background: active ? "var(--color-background-secondary)" : "var(--color-background-primary)",
        color: active ? "var(--color-text-primary)" : "var(--color-text-secondary)",
      }}
    >
      {label}{count > 0 ? ` ${count}` : ""}
    </button>
  );
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <div>
      <div style={{ fontSize: 10, color: "var(--color-text-tertiary)", textTransform: "lowercase", marginBottom: 5 }}>{title}</div>
      <div style={{ display: "flex", flexDirection: "column", gap: 4 }}>{children}</div>
    </div>
  );
}

function Field({ label, value, tone = "muted" }: { label: string; value: string; tone?: Tone }) {
  return (
    <div style={{ display: "flex", justifyContent: "space-between", gap: 10, fontSize: 11 }}>
      <span style={{ color: "var(--color-text-tertiary)" }}>{label}</span>
      <span style={{ ...mono, ...ellipsis, color: tone === "muted" ? "var(--color-text-secondary)" : toneColor[tone], textAlign: "right" }}>{value}</span>
    </div>
  );
}
