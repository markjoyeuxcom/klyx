import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, act, fireEvent, within } from "@testing-library/react";

// The cert-manager lens and the TLS consumer, mounted on the REAL rows
// adapter (health/useCertManagerRows) and health store - only the transport
// is stubbed (Codex review of 9de9d73: the adapter/detail paths are tested
// end to end, not through a seeded slice).
vi.mock("../bridge/healthRows", () => ({ openRows: vi.fn(() => () => {}) }));
vi.mock("../bridge/certmanager", () => ({ getCertificateChain: vi.fn(async () => {}) }));
vi.mock("../bridge/ai", () => ({
  aiAvailability: vi.fn(async () => ({ backend: "off", ready: false })),
  aiEgressAllowed: vi.fn(async () => false),
  explainCertificate: vi.fn(),
}));
vi.mock("../bridge/ruleMetrics", () => ({ ruleMetrics: () => Object.assign(Promise.resolve({ status: { available: false, message: "", updatedAt: "" }, scope: "", start: "", end: "", stepSeconds: 30, rateSeconds: 60, routes: [], rules: [], backendSplit: "", notes: [] }), { cancel: () => {} }) }));
vi.mock("../bridge/gateway", () => ({ getGatewayTopology: vi.fn(async () => {}), getRouteMetrics: vi.fn(async () => {}), abandonRouteMetrics: vi.fn() }));
import { getCertificateChain } from "../bridge/certmanager";
import { CertsView } from "./CertsView";
import { GatewayTopology } from "./GatewayTopology";
import { _resetCertManagerRowsForTest } from "../health/useCertManagerRows";
import { resetHealthStore, addSub, openStarted, openSucceeded, applyRows, applySummary, dueForBootstrap, bootstrapSettled, useHealth, type HealthPart, type HealthSummary, type KindCoverage, type RowsDTO } from "../store/health";
import { useFleet, type CertChainDTO, type CertificateDTO, type ClusterDTO, type IssuerDTO, type TopologyDTO } from "../store/fleet";
import { certRule, issuerRule, withRule } from "../test/backendRules";
import { clock } from "../layout/coverage";

const cert = (name: string, over: Partial<CertificateDTO> = {}): CertificateDTO => withRule<CertificateDTO>({
  namespace: "payments", name, ready: "Ready", reason: "", message: "", issuerKind: "ClusterIssuer", issuerName: "le",
  secretName: `${name}-tls`, dnsNames: [], notBeforeUnix: 0, notAfterUnix: 0, expiresInSeconds: 0, renewalUnix: 0, uid: `uid-${name}`, ...over,
}, certRule);
const issuer = (name: string, over: Partial<IssuerDTO> = {}): IssuerDTO => withRule<IssuerDTO>({
  kind: "ClusterIssuer", namespace: "", name, ready: "Ready", reason: "", message: "", ...over,
}, issuerRule);

const kind = (k: string, over: Partial<KindCoverage> = {}): KindCoverage => ({ kind: k, role: "counted", scope: "", coverage: "live", objects: 1, ...over });
const CERT_KINDS = () => [kind("certificates")];
const ISSUER_KINDS = () => [kind("issuers"), kind("clusterissuers")];
const part = (id: string, ver: number, kinds: KindCoverage[], native: { total: number; included: number }): HealthPart => ({
  id, entity: id, scope: "", classifier: "native", coverage: "live", kinds, obs: ver, ver,
  native: { total: native.total, included: native.included, word: "", roles: { total: native.total, fault: native.included, degraded: 0, unknown: 0, progressing: 0, paused: 0, ok: native.total - native.included } },
});
const rowsDTO = (incarnation: number, id: string, ver: number, rows: unknown[], kinds: KindCoverage[], evaluatedAt = "2026-10-11T12:00:00Z"): RowsDTO => ({
  cluster: "x", incarnation, task: "certs", part: part(id, ver, kinds, { total: rows.length, included: (rows as { rule: { included: boolean } }[]).filter((r) => r.rule.included).length }), evaluatedAt, rows,
});
const summary = (incarnation: number, seq: number, certs: [number, number, number], issuers: [number, number, number], final = false): HealthSummary => ({
  cluster: "x", incarnation, seq, evaluatedAt: "", connection: "connected", final,
  tasks: [{ id: "certs", coverage: "live", parts: [part("certificates", certs[0], CERT_KINDS(), { total: certs[1], included: certs[2] }), part("issuers", issuers[0], ISSUER_KINDS(), { total: issuers[1], included: issuers[2] })] }],
});

const clusterDTO: ClusterDTO = {
  name: "x", state: "Synced", reason: "", nodesReady: 1, nodesTotal: 1, pods: 1, version: "v1",
  gitopsTier: "Absent", gitopsReason: "", networkTier: "Absent", networkReason: "",
  certManagerTier: "Healthy", env: "", region: "", provider: "", group: "", ageSeconds: 0,
};

// Two subscriptions stand in for the transport's opens (one per part).
function open(incarnation = 1) {
  for (const [h, id] of [["c", "certificates"], ["i", "issuers"]] as const) {
    if (!useHealth.getState().subs[h]) addSub(h, "x", "certs", id);
    openStarted(h, `tok-${h}-${incarnation}`);
    openSucceeded(h, `tok-${h}-${incarnation}`, incarnation);
  }
}

const gateway = { namespace: "infra", name: "eg" };
const topo = (secret: string): TopologyDTO => ({
  routesComplete: true,
  gateway: { namespace: "infra", name: "eg", className: "eg", accepted: true, programmed: true, generation: 1, addresses: [], policies: [],
    listeners: [{ name: "https", protocol: "HTTPS", hostname: "", port: 443, certificateRefs: [{ group: "", kind: "Secret", namespace: "payments", name: secret }] }],
    rule: { included: false, role: "ok", label: "programmed" } },
  routes: [{
    namespace: "apps", name: "web", hostnames: [], matches: [], parentSectionName: "https", accepted: true, resolvedRefs: true, generation: 1, parentFound: true,
    backends: [], services: [], pods: { ready: 1, total: 1, unknown: false }, policies: [], rule: { included: false, role: "ok", label: "ok" },
  }],
} as unknown as TopologyDTO);

function seedFleet(secret = "web-tls") {
  useFleet.setState({
    clusters: [clusterDTO],
    route: { name: "cluster", cluster: "x", section: "certs" },
    network: { cluster: "x", selected: gateway, topology: topo(secret), topologyLoading: false, topologyError: null, topologyAt: Date.now(), routesAt: Date.now(), selectedRoute: "apps/web", routeMetrics: {}, routeMetricsStatus: null, routeMetricsStale: false },
  });
}

// the listener's TLS line: the Secret ref and its certificate status
const tls = () => screen.getByText(/^Secret payments\//).parentElement!.textContent ?? "";
const headline = () => document.querySelector(".lens-header .t-number")?.textContent;
const chip = (re: RegExp) => screen.getByRole("button", { name: re }).textContent;

beforeEach(() => {
  vi.clearAllMocks();
  resetHealthStore();
  _resetCertManagerRowsForTest();
  useFleet.getState().clearCertManager();
  seedFleet();
});

describe("cert-manager rows, mounted end to end (Codex review of 9de9d73)", () => {
  // #1: past A4 the totals and attention counts are the summary's in every
  // surface; the breakdown stays row-scoped and says so; the A4 notice and a
  // simultaneous read error stack, neither hiding the other.
  it("A4 moves the global counts with the notice, in both views, beside a read error", () => {
    vi.useFakeTimers({ now: 0 });
    try {
      open();
      applyRows("c", rowsDTO(1, "certificates", 1, [cert("web"), cert("api", { ready: "Failed", reason: "Boom" })], CERT_KINDS()), 0);
      applyRows("i", rowsDTO(1, "issuers", 1, [issuer("le"), issuer("zerossl", { ready: "False" })], [kind("issuers"),
        kind("clusterissuers", { coverage: "stale", class: "forbidden", error: "rbac", lastGoodAt: "2026-10-11T11:00:00Z" })]), 0);
      render(<CertsView cluster="x" />);
      expect(chip(/^Not ready/)).toBe("Not ready 1");
      // the summary moves ahead: certificates 5 (3 not ready), issuers 4 (2 not ready)
      act(() => { applySummary(summary(1, 2, [2, 5, 3], [2, 4, 2]), 0); dueForBootstrap(1500); });
      expect(screen.getByTestId("rows-refreshing").textContent).toMatch(/Rows as of .* refreshing; the totals are the cluster summary's/);
      expect(screen.getByRole("alert").textContent).toMatch(/watch failed - showing the last good state from .*rbac/); // stacked, not replaced
      expect(headline()).toBe("5"); // the headline total
      expect(screen.getByText(/^▲ 3 not ready or expired$/)).toBeTruthy();
      expect(chip(/^Not ready/)).toBe("Not ready 3");
      expect(chip(/^Issuers/)).toBe("Issuers 2");
      expect(screen.getByTestId("cert-breakdown").textContent).toMatch(/^1 failed or expired \(as of /);
      expect(document.querySelectorAll(".lens-row")).toHaveLength(2); // the rows in hand, as of their capture
      fireEvent.click(screen.getByRole("button", { name: /^Issuers/ }));
      expect(headline()).toBe("4*"); // the summary's issuer total, last observed (stale kind)
      expect(screen.getByText("2 not ready")).toBeTruthy();
      expect(chip(/^Not ready/)).toBe("Not ready 2");
      expect(screen.getByTestId("rows-refreshing")).toBeTruthy();
      expect(screen.getByRole("alert")).toBeTruthy();
      // a failed re-read is named in the notice
      const gen = useHealth.getState().subs.c.requestGen;
      act(() => bootstrapSettled("c", gen, { error: "capture timed out" }, 1600));
      expect(screen.getByTestId("rows-refreshing").textContent).toMatch(/Last re-read failed: capture timed out/);
    } finally {
      vi.useRealTimers();
    }
  });

  // #2: a Final fences the previous incarnation's rows in every consumer; a
  // new incarnation's parts land one at a time, each pending until its own
  // rows arrive - never the other part's evidence, never the old rows.
  it("Final(1) -> summary(2) -> staggered parts: no consumer keeps incarnation 1's rows", () => {
    open();
    applyRows("c", rowsDTO(1, "certificates", 1, [cert("old")], CERT_KINDS()));
    applyRows("i", rowsDTO(1, "issuers", 1, [issuer("le")], ISSUER_KINDS()));
    seedFleet("old-tls");
    render(<><CertsView cluster="x" /><GatewayTopology cluster="x" gateway={gateway} /></>);
    expect(screen.getByText("old")).toBeTruthy();
    expect(tls()).toMatch(/certificate ready/);

    act(() => applySummary({ ...summary(1, 9, [1, 1, 0], [1, 1, 0]), final: true }));
    expect(screen.queryByText("old")).toBeNull();
    expect(screen.getByLabelText("loading certificate state")).toBeTruthy();
    expect(tls()).toMatch(/Certificates not read/);

    act(() => applySummary(summary(2, 1, [1, 2, 0], [1, 1, 0])));
    expect(screen.queryByText("old")).toBeNull();
    expect(tls()).toMatch(/Certificates not read/);

    act(() => { open(2); applyRows("c", rowsDTO(2, "certificates", 1, [cert("old"), cert("new")], CERT_KINDS())); });
    expect(screen.getByText("new")).toBeTruthy();
    expect(tls()).toMatch(/certificate ready/); // incarnation 2's own Certificate read
    fireEvent.click(screen.getByRole("button", { name: /^Issuers/ }));
    expect(screen.getByLabelText("loading certificate state")).toBeTruthy(); // the issuers part has not answered
    expect(useFleet.getState().certmanager.issuers).toEqual([]);

    act(() => { applyRows("i", rowsDTO(2, "issuers", 1, [issuer("le2")], ISSUER_KINDS())); });
    expect(screen.getByText("le2")).toBeTruthy();
    expect(screen.queryByText("le")).toBeNull();
  });

  // #3: a discovery gap beside live data is its own notice and never stars
  // the rows; a stale kind names its last good time in the banner and in the
  // TLS consumer.
  it("discovery gaps and last-good times are surfaced, without downgrading live rows", () => {
    open();
    applyRows("c", rowsDTO(1, "certificates", 1, [cert("web")], [kind("certificates", { discovery: "forbidden: discovery re-check" })]));
    applyRows("i", rowsDTO(1, "issuers", 1, [issuer("le")], ISSUER_KINDS()));
    render(<><CertsView cluster="x" /><GatewayTopology cluster="x" gateway={gateway} /></>);
    expect(screen.getByTestId("discovery-gap").textContent).toMatch(/Certificate: forbidden: discovery re-check/);
    expect(screen.getByTestId("cert-discovery-gap")).toBeTruthy();
    expect(headline()).toBe("1"); // the live total, unstarred
    expect(screen.queryByRole("alert")).toBeNull();
    expect(tls()).toMatch(/certificate ready(?! \()/);

    act(() => { applyRows("c", rowsDTO(1, "certificates", 2, [cert("web")], CERT_KINDS())); }); // the re-check recovers
    expect(screen.queryByTestId("discovery-gap")).toBeNull();
    expect(screen.queryByTestId("cert-discovery-gap")).toBeNull();

    const lastGood = "2026-10-11T09:30:00Z";
    act(() => { applyRows("c", rowsDTO(1, "certificates", 3, [cert("web")], [kind("certificates", { coverage: "stale", class: "transport", error: "dial tcp", lastGoodAt: lastGood })])); });
    expect(screen.getByRole("alert").textContent).toContain(`showing the last good state from ${clock(lastGood)}`);
    expect(headline()).toBe("1*");
    expect(tls()).toContain(`certificate ready (last observed ${clock(lastGood)})`);
  });

  // §5a: a selection missing from a read that is not complete for its kind
  // (here: fenced by a Final, the new incarnation still checking) is kept with
  // its last row, unverified - never removed, never silently closed.
  it("a selection missing from an incomplete read is kept, unverified", () => {
    open();
    applyRows("c", rowsDTO(1, "certificates", 1, [cert("web")], CERT_KINDS()));
    applyRows("i", rowsDTO(1, "issuers", 1, [issuer("le")], ISSUER_KINDS()));
    render(<CertsView cluster="x" />);
    act(() => useFleet.getState().selectCertificate("payments/web"));
    act(() => applySummary({ ...summary(1, 9, [1, 1, 0], [1, 1, 0]), final: true }));
    const panel = within(screen.getByTestId("cert-panel"));
    expect(panel.getByTestId("selection-unverified").textContent).toMatch(/Not in the current Certificate read\..*unverified/);
    expect(panel.queryByText(/Certificate removed\./)).toBeNull();
    act(() => { open(2); applyRows("c", rowsDTO(2, "certificates", 1, [], CERT_KINDS())); });
    expect(within(screen.getByTestId("cert-panel")).getByText(/Certificate removed\./)).toBeTruthy(); // a complete read without it
  });

  // #5: a failed chain read is the request's outcome; a chain is the row's
  // only by UID; the last chain is kept by UID for a removal or a failed
  // re-read, never across a recreation, which is read afresh.
  describe("chain results", () => {
    const chainOf = (uid: string, warning: string, asOfUnix = 1_760_000_000): CertChainDTO => ({
      asOfUnix, certificate: { ...cert("web"), uid }, issuer: null, requests: [], failingLink: null, warnings: [warning],
    });
    const errorChain = (msg: string): CertChainDTO => ({
      error: msg, certificate: { namespace: "payments", name: "web" } as CertificateDTO, issuer: null, requests: [], failingLink: null, warnings: [],
    });
    const mount = (certs: CertificateDTO[]) => {
      open();
      applyRows("c", rowsDTO(1, "certificates", 1, certs, CERT_KINDS()));
      applyRows("i", rowsDTO(1, "issuers", 1, [issuer("le")], ISSUER_KINDS()));
      render(<CertsView cluster="x" />);
      act(() => useFleet.getState().selectCertificate("payments/web"));
    };
    const panel = () => within(screen.getByTestId("cert-panel"));

    it("a failed read is shown as the request's error, never an empty chain", () => {
      mount([cert("web")]);
      act(() => useFleet.getState().setCertChain(errorChain("certificates.cert-manager.io \"web\" is forbidden"), "x"));
      expect(panel().getByTestId("chain-error").textContent).toMatch(/Chain read failed: .*forbidden - the chain is unverified, not empty/);
      expect(panel().queryByText("No active issuance request.")).toBeNull();
      expect(panel().queryByText("Resolving chain…")).toBeNull();
    });

    it("a chain without the selected row's UID is not its detail", () => {
      mount([cert("web")]);
      act(() => useFleet.getState().setCertChain(chainOf("", "uid-less-chain"), "x"));
      expect(panel().queryAllByText("uid-less-chain")).toHaveLength(0);
      act(() => useFleet.getState().setCertChain(chainOf("uid-web", "this-objects-chain"), "x"));
      expect(panel().getAllByText("this-objects-chain").length).toBeGreaterThan(0);
    });

    it("a failed re-read keeps the same object's last chain, labelled as such", () => {
      mount([cert("web")]);
      act(() => useFleet.getState().setCertChain(chainOf("uid-web", "resolved-before"), "x"));
      act(() => useFleet.getState().setCertChain(errorChain("context deadline exceeded"), "x"));
      expect(panel().getByTestId("chain-error").textContent).toMatch(/Chain read failed: context deadline exceeded$/);
      expect(panel().getAllByText("resolved-before").length).toBeGreaterThan(0);
      expect(panel().getByText(/^Last resolved /)).toBeTruthy();
    });

    // The Line slice 4 §6.3 (design review 37f00bc3): a recreation is
    // "Replaced" - the pinned certificate keeps its last chain and nothing is
    // read for the new one until "Open the new object"; then the old chain is
    // never shown as the new object's.
    it("a removal keeps the last chain by UID; a recreation is Replaced, and opening it reads its chain afresh", () => {
      mount([cert("web")]);
      act(() => useFleet.getState().setCertChain(chainOf("uid-web", "first-objects-chain"), "x"));
      act(() => { applyRows("c", rowsDTO(1, "certificates", 2, [], CERT_KINDS())); }); // removed
      expect(panel().getByText(/Certificate removed\./)).toBeTruthy();
      expect(panel().getAllByText("first-objects-chain").length).toBeGreaterThan(0);
      expect(panel().getByText(/before it was removed/)).toBeTruthy();

      const before = (getCertificateChain as ReturnType<typeof vi.fn>).mock.calls.length;
      act(() => { applyRows("c", rowsDTO(1, "certificates", 3, [cert("web", { uid: "uid-web-2" })], CERT_KINDS())); }); // recreated
      expect(panel().getByText("Replaced.")).toBeTruthy();
      expect(panel().getAllByText("first-objects-chain").length).toBeGreaterThan(0); // the pinned object's last evidence
      expect((getCertificateChain as ReturnType<typeof vi.fn>).mock.calls.length).toBe(before); // nothing read for the new one
      fireEvent.click(panel().getByRole("button", { name: "Open the new object" }));
      expect(panel().queryAllByText("first-objects-chain")).toHaveLength(0);
      expect(panel().getByTestId("chain-unresolved").textContent).toMatch(/earlier object with this name/);
      expect((getCertificateChain as ReturnType<typeof vi.fn>).mock.calls.length).toBe(before + 1); // the new object's chain is requested
    });
  });
});

// Health 1c slice 4 §5.4: a summary Top link reveals its object - the part's
// view, past the filters, selected; selection only.
describe("a Top link reveals its object", () => {
  const selectedRow = () => document.querySelector('[data-selected="true"]')?.textContent ?? "";
  const reveal = (part: string, ref: { gvr: string; namespace?: string; name: string }) =>
    act(() => { useFleet.getState().requestNavIntent({ kind: "reveal", cluster: "x", task: "certs", part, ref }); });

  it("an Issuer opens the issuers view and is selected; a Certificate is selected in its view", () => {
    open();
    applyRows("c", rowsDTO(1, "certificates", 1, [cert("web"), cert("api", { ready: "Failed", reason: "Boom" })], CERT_KINDS()), 0);
    applyRows("i", rowsDTO(1, "issuers", 1, [issuer("le"), issuer("zerossl", { ready: "False" })], ISSUER_KINDS()), 0);
    render(<CertsView cluster="x" />);
    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "nothing-matches" } });
    reveal("issuers", { gvr: "cert-manager.io/v1/clusterissuers", name: "zerossl" });
    expect(screen.getByRole("button", { name: /^Issuers/ }).getAttribute("aria-pressed")).toBe("true");
    expect(selectedRow()).toContain("zerossl");
    expect(useFleet.getState().navIntent).toBeNull();
    reveal("certificates", { gvr: "cert-manager.io/v1/certificates", namespace: "payments", name: "web" });
    expect(screen.getByRole("button", { name: /^Certificates/ }).getAttribute("aria-pressed")).toBe("true");
    expect(useFleet.getState().certmanager.selectedKey).toBe("payments/web");
    expect(useFleet.getState().navIntent).toBeNull();
  });

  it("an object gone from a complete read is reported, never a silent no-op; another task's reveal is left alone", () => {
    open();
    applyRows("c", rowsDTO(1, "certificates", 1, [cert("web")], CERT_KINDS()), 0);
    applyRows("i", rowsDTO(1, "issuers", 1, [issuer("le")], ISSUER_KINDS()), 0);
    render(<CertsView cluster="x" />);
    act(() => { useFleet.getState().requestNavIntent({ kind: "reveal", cluster: "x", task: "eso", part: "stores", ref: { gvr: "external-secrets.io/v1/secretstores", name: "s" } }); });
    expect(useFleet.getState().navIntent).toMatchObject({ task: "eso" });
    reveal("issuers", { gvr: "cert-manager.io/v1/clusterissuers", name: "gone" });
    expect(useFleet.getState().actionStatus).toMatchObject({ kind: "error", message: expect.stringMatching(/gone is no longer present/) });
  });
});

// The Line slice 9 note §4.2: a reveal naming its object by UID pins that
// UID; another object at the name, or none, is said - never shown.
describe("a reveal by UID", () => {
  const reveal = (part: string, ref: { gvr: string; namespace?: string; name: string; uid: string }) =>
    act(() => { useFleet.getState().requestNavIntent({ kind: "reveal", cluster: "x", task: "certs", part, ref }); });
  const gone = () => document.querySelector("[data-pinned-gone]")?.getAttribute("data-pinned-gone") ?? null;

  it("a Certificate with that UID opens; another UID at the name is Replaced, with the new one offered", () => {
    open();
    applyRows("c", rowsDTO(1, "certificates", 1, [cert("web"), cert("api", { uid: "uid-api-2" })], CERT_KINDS()), 0);
    applyRows("i", rowsDTO(1, "issuers", 1, [issuer("le")], ISSUER_KINDS()), 0);
    render(<CertsView cluster="x" />);
    reveal("certificates", { gvr: "cert-manager.io/v1/certificates", namespace: "payments", name: "web", uid: "uid-web" });
    expect(gone()).toBeNull();
    expect(useFleet.getState().certmanager.selectedKey).toBe("payments/web");
    reveal("certificates", { gvr: "cert-manager.io/v1/certificates", namespace: "payments", name: "api", uid: "uid-api" });
    expect(gone()).toBe("replaced");
    expect(useFleet.getState().navIntent).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Open the new object" }));
    expect(gone()).toBeNull();
  });

  it("absent: unverified on an incomplete read, removed on a complete one - never a toast", () => {
    useFleet.setState({ actionStatus: null });
    open();
    applyRows("c", rowsDTO(1, "certificates", 1, [cert("web")], [kind("certificates", { coverage: "stale", error: "dial tcp" })]), 0);
    applyRows("i", rowsDTO(1, "issuers", 1, [issuer("le")], ISSUER_KINDS()), 0);
    render(<CertsView cluster="x" />);
    reveal("certificates", { gvr: "cert-manager.io/v1/certificates", namespace: "payments", name: "gone", uid: "uid-gone" });
    expect(gone()).toBe("unverified");
    expect(useFleet.getState().actionStatus).toBeNull();
    act(() => { applyRows("c", rowsDTO(1, "certificates", 2, [cert("web")], CERT_KINDS()), 0); });
    expect(gone()).toBe("removed");
  });
});
