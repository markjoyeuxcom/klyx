import { describe, it, expect, vi } from "vitest";
import { render, fireEvent, within, act, screen } from "@testing-library/react";
import { useFleet } from "../store/fleet";
import type { CertificateDTO, IssuerDTO, CertChainDTO, ClusterDTO } from "../store/fleet";
import { CertsView } from "./CertsView";

// The rows hook is the transport; these tests seed the certmanager slice it fills.
vi.mock("../health/useCertManagerRows", () => ({ useCertManagerRows: () => {} }));
vi.mock("../bridge/certmanager", () => ({
  openCertManager: async () => () => {},
  closeCertManager: async () => {},
  getCertificateChain: async () => {},
}));

vi.mock("../bridge/ai", () => ({
  aiAvailability: vi.fn(async () => ({ backend: "off", ready: false })),
  aiEgressAllowed: vi.fn(async () => false),
  explain: vi.fn(async () => ({ ok: true, explanation: "", model: "", backend: "", groundedOn: "", error: "" })),
}));
import { aiAvailability, aiEgressAllowed, explain } from "../bridge/ai";
import { certRule, issuerRule, withRule } from "../test/backendRules";

// drawnBeforeActions: the element sits in the inspector's drawing slot, ahead
// of its action band (The Line slice 13c.3, parent §4).
function drawnBeforeActions(el: HTMLElement): boolean {
  const panel = el.closest('[data-testid="lens-inspector"]') ?? document.body;
  const actions = panel.querySelector('[role="group"][aria-label="actions"]');
  return !!el.closest(".insp-drawing") && !!actions && !!(el.compareDocumentPosition(actions) & Node.DOCUMENT_POSITION_FOLLOWING);
}

const cluster = (tier: string): ClusterDTO => ({
  name: "x", state: "Synced", reason: "", nodesReady: 1, nodesTotal: 1, pods: 1, version: "v1",
  gitopsTier: "Absent", gitopsReason: "", networkTier: "Absent", networkReason: "",
  certManagerTier: tier, env: "", region: "", provider: "", group: "", ageSeconds: 0,
});

// Fixtures carry the rule the backend computes (test/backendRules.ts, held to
// the Go golden fixture), stamped at build time - the lens only reads it.
const cert = (over: Partial<CertificateDTO>): CertificateDTO => withRule<CertificateDTO>({
  namespace: "payments", name: "acme-demo", ready: "Ready", reason: "", message: "",
  issuerKind: "ClusterIssuer", issuerName: "lab-acme-staging", secretName: "acme-demo-tls",
  dnsNames: ["acme-demo.klustr.io"], notBeforeUnix: 0, notAfterUnix: 0, expiresInSeconds: 0, renewalUnix: 0, ...over,
}, certRule);

const issuer = (over: Partial<IssuerDTO>): IssuerDTO => withRule<IssuerDTO>({
  kind: "ClusterIssuer", namespace: "", name: "lab-acme-staging", ready: "Ready", reason: "", message: "", ...over,
}, issuerRule);

function setup(tier: string, certs: CertificateDTO[], issuers: IssuerDTO[], selectedKey: string | null = null, chain: CertChainDTO | null = null) {
  useFleet.setState({
    clusters: [cluster(tier)],
    route: { name: "cluster", cluster: "x", section: "certs" },
    certmanager: { cluster: "x", certificates: certs, issuers, loading: false, selectedKey, chain, chainLoading: false, chainCluster: chain ? "x" : null },
  });
}

const insp = () => within(screen.getByTestId("lens-inspector"));
const rowsText = () => [...document.querySelectorAll(".lens-row")].map((r) => r.textContent ?? "");

describe("CertsView", () => {
  it("the Issuers view reads the rule: attention filter, count and verdict label (Codex 2b-1 #1)", () => {
    setup("Healthy", [], [
      issuer({ name: "flagged", ready: "Ready", rule: { included: true, role: "fault", label: "Flagged by backend" } }),
      issuer({ name: "cleared", ready: "Failed", reason: "ErrInit", rule: { included: false, role: "ok", label: "Ready" } }),
    ]);
    render(<CertsView cluster="x" />);
    fireEvent.click(screen.getByRole("button", { name: /^Issuers/ }));
    expect(screen.getByText("flagged").closest(".lens-row")?.textContent).toContain("Flagged by backend");
    expect(screen.getByText("cleared").closest(".lens-row")?.textContent).not.toContain("Failed");
    fireEvent.click(screen.getByRole("button", { name: /^Not ready/ }));
    expect(screen.getByRole("button", { name: /^Not ready/ }).textContent).toBe("Not ready 1");
    expect(rowsText().map((r) => r.includes("flagged"))).toEqual([true]); // count and rows agree
  });

  it("reads the Go-computed rule, never its own: verdict, label and the Not ready count (1c §2)", () => {
    // The rules contradict the native fields on purpose: only the rule may decide.
    setup("Healthy", [
      cert({ name: "backend-flags", ready: "Ready", rule: { included: true, role: "fault", label: "Flagged by backend" } }),
      cert({ name: "backend-clears", ready: "Failed", rule: { included: false, role: "ok", label: "Ready" } }),
    ], [issuer({ ready: "Failed", rule: { included: false, role: "ok", label: "Ready" } })]);
    render(<CertsView cluster="x" />);
    expect(screen.getByText("backend-flags").closest(".lens-row")?.textContent).toContain("Flagged by backend");
    expect(screen.getByText("backend-clears").closest(".lens-row")?.textContent).not.toContain("Failed");
    expect(screen.getByRole("button", { name: /^Not ready/ }).textContent).toBe("Not ready 1");
    expect(screen.queryByText(/issuers? not ready/)).toBeNull();
  });

  it("a stale certificate dims and shows the Syncing chip", () => {
    setup("Healthy", [cert({ name: "edited-tls", ready: "Ready", stale: true, generation: 3, observedGeneration: 2 })], [issuer({})]);
    const { getByText, getByTitle } = render(<CertsView cluster="x" />);
    const row = getByText("edited-tls").closest(".lens-row") as HTMLElement;
    expect(getByText("Syncing… generation 3, observed 2")).toBeTruthy();
    expect(row.textContent).not.toContain("Ready");
    expect(getByTitle(/Observed Gen: 2 vs Current: 3/)).toBeTruthy();
    expect(row.dataset.stale).toBe("true"); // dims every cell but the row's identity (layout.css)
  });


  it("renders an honest empty state when cert-manager is absent", () => {
    setup("Absent", [], []);
    const { getByText } = render(<CertsView cluster="x" />);
    expect(getByText(/cert-manager is not installed on this cluster/i)).toBeTruthy();
  });

  it("sorts failing certificates first (broken-first triage)", () => {
    setup("Healthy", [
      cert({ name: "healthy-cert", ready: "Ready" }),
      cert({ name: "broken-cert", ready: "Failed", reason: "DoesNotExist" }),
    ], []);
    render(<CertsView cluster="x" />);
    const rows = rowsText();
    expect(rows[0]).toContain("broken-cert");
    expect(rows[1]).toContain("healthy-cert");
  });

  it("the inspector opens with the shared verdict: state word plus reason, then the chain", () => {
    const c = cert({ name: "acme-demo", ready: "Failed", reason: "DoesNotExist" });
    setup("Healthy", [c], [issuer({})], "payments/acme-demo");
    render(<CertsView cluster="x" />);
    expect(insp().getByText("Failed")).toBeTruthy();
    expect(insp().getAllByText(/DoesNotExist/).length).toBeGreaterThan(0);
    expect(insp().getByText(/^Issuance chain/)).toBeTruthy();
    expect(drawnBeforeActions(insp().getByText(/^Issuance chain/))).toBe(true);
  });

  it("renders the deepest stuck node's message in the flagged box", () => {
    const c = cert({ name: "acme-demo", ready: "Issuing", reason: "DoesNotExist" });
    const chain: CertChainDTO = {
      certificate: c,
      issuer: issuer({}),
      requests: [{
        kind: "CertificateRequest", namespace: "payments", name: "acme-demo-1", ready: "Failed", reason: "Pending",
        message: "propagation check failed: NXDOMAIN", children: [],
      }],
      failingLink: {
        kind: "CertificateRequest", namespace: "payments", name: "acme-demo-1", ready: "Failed", reason: "Pending",
        message: "propagation check failed: NXDOMAIN", children: [],
      },
      warnings: [],
    };
    setup("Healthy", [c], [issuer({})], "payments/acme-demo", chain);
    render(<CertsView cluster="x" />);
    const inspector = screen.getByTestId("lens-inspector");
    expect(within(inspector).getByText(/deepest stuck node flagged/i)).toBeTruthy();
    expect(within(inspector).getByText("propagation check failed: NXDOMAIN")).toBeTruthy();
    // the chain's text roles (slice 13c review): names in the name role, no
    // caller-sized text, the stuck node's tone kept on its mark and tag
    const rail = inspector.querySelector("ol[aria-label='Issuance chain']") as HTMLElement;
    expect(rail.querySelectorAll(".rail-name").length).toBeGreaterThan(0);
    expect(rail.querySelectorAll('[style*="font-size"]')).toHaveLength(0);
  });

  it("headlines a failing bound issuer in the inspector", () => {
    const c = cert({ name: "acme-demo", ready: "Failed", reason: "DoesNotExist" });
    setup("Healthy", [c], [issuer({ ready: "False", reason: "ACMEAccountRegistrationFailed" })], "payments/acme-demo");
    render(<CertsView cluster="x" />);
    const inspector = screen.getByTestId("lens-inspector");
    expect(within(inspector).getByText(/issuer not ready: ACMEAccountRegistrationFailed/i)).toBeTruthy();
  });

  it("renders the issuance chain and flags the stuck link", () => {
    const c = cert({ name: "acme-demo", ready: "Issuing", reason: "DoesNotExist" });
    const chain: CertChainDTO = {
      certificate: c,
      issuer: issuer({ ready: "False", reason: "ACMEAccountRegistrationFailed" }),
      requests: [{
        kind: "CertificateRequest", namespace: "payments", name: "acme-demo-1", ready: "Failed", reason: "Pending", message: "", children: [{
          kind: "Order", namespace: "payments", name: "acme-demo-1-123", ready: "Failed", reason: "order errored", message: "", children: [{
            kind: "Challenge", namespace: "payments", name: "acme-demo-1-123-456", ready: "Issuing", reason: "DNS-01 propagation", message: "", children: [],
          }],
        }],
      }],
      failingLink: { kind: "Challenge", namespace: "payments", name: "acme-demo-1-123-456", ready: "Issuing", reason: "DNS-01 propagation", message: "", children: [] },
      warnings: [],
    };
    setup("Healthy", [c], [], "payments/acme-demo", chain);
    render(<CertsView cluster="x" />);
    const inspector = screen.getByTestId("lens-inspector");
    expect(within(inspector).getByText("acme-demo-1-123-456")).toBeTruthy();
    expect(within(inspector).getByText(/stuck here/i)).toBeTruthy();
    // The server-resolved bound issuer drives the headline - it IS the likely
    // cause, so a second "likely cause" box no longer repeats it.
    expect(within(inspector).getByText(/issuer not ready: ACMEAccountRegistrationFailed/i)).toBeTruthy();
    expect(within(inspector).queryByText(/likely cause/i)).toBeNull();
  });

  it("filters certificates by issuing, issuer problem, and namespace", () => {
    setup("Healthy", [
      cert({ namespace: "payments", name: "issuing-cert", ready: "Issuing" }),
      cert({ namespace: "cnpg-system", name: "ready-cert", ready: "Ready", issuerName: "missing-issuer" }),
    ], [issuer({})]);
    const { getByRole, queryByText, getByLabelText } = render(<CertsView cluster="x" />);
    fireEvent.click(getByRole("button", { name: /^Issuing \d+$/ })); // the chip, not a row whose lamp reads Issuing
    expect(rowsText().join(" ")).toContain("issuing-cert");
    expect(queryByText("ready-cert")).toBeNull();

    fireEvent.click(getByRole("button", { name: /^Issuer problem/ }));
    expect(rowsText().join(" ")).toContain("ready-cert");

    fireEvent.change(getByLabelText("Namespace"), { target: { value: "payments" } });
    expect(queryByText("ready-cert")).toBeNull();
  });

  it("shows validity timeline and missing secret cause", () => {
    const c = cert({
      name: "secret-missing",
      ready: "Failed",
      reason: "DoesNotExist",
      notBeforeUnix: 1780272000,
      notAfterUnix: 1782864000,
      renewalUnix: 1782259200,
    });
    setup("Healthy", [c], [issuer({})], "payments/secret-missing");
    render(<CertsView cluster="x" />);
    const inspector = screen.getByTestId("lens-inspector");
    expect(within(inspector).getByText(/likely cause/i)).toBeTruthy();
    expect(within(inspector).getByText(/target secret acme-demo-tls/i)).toBeTruthy();
    expect(within(inspector).getByText("2026-06-01")).toBeTruthy();
    expect(within(inspector).getByText("2026-06-24")).toBeTruthy();
  });

  it("keeps rendering when a chain payload has null arrays", () => {
    const c = cert({ namespace: "cnpg-system", name: "cnpg-ca", dnsNames: null as unknown as string[] });
    const chain: CertChainDTO = {
      certificate: c,
      issuer: null,
      requests: [{
        kind: "CertificateRequest", namespace: "cnpg-system", name: "cnpg-ca-1", ready: "Ready", reason: "", message: "",
        children: null as unknown as [],
      }],
      failingLink: null,
      warnings: null as unknown as string[],
    };
    setup("Healthy", [c], [], "cnpg-system/cnpg-ca", chain);
    render(<CertsView cluster="x" />);
    const inspector = screen.getByTestId("lens-inspector");
    expect(within(inspector).getByRole("heading", { name: "cnpg-ca" })).toBeTruthy();
    expect(within(inspector).getByText("cnpg-ca-1")).toBeTruthy();
  });

  // --- Keyboard nav ---

  it("j/k + Enter selects the certificate like a click, Esc clears the selection", () => {
    setup("Healthy", [cert({ name: "a-cert" }), cert({ name: "b-cert" })], [issuer({})]);
    render(<CertsView cluster="x" />);
    // j → index 0 (a-cert); j → index 1 (b-cert); Enter → select(keyOf(b-cert))
    act(() => { window.dispatchEvent(new KeyboardEvent("keydown", { key: "j", bubbles: true, cancelable: true })); });
    act(() => { window.dispatchEvent(new KeyboardEvent("keydown", { key: "j", bubbles: true, cancelable: true })); });
    act(() => { window.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true })); });
    expect(useFleet.getState().certmanager.selectedKey).toBe("payments/b-cert");
    // Esc clears the selection.
    act(() => { window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true })); });
    expect(useFleet.getState().certmanager.selectedKey).toBeNull();
  });

  it("/ focuses the certificates filter input", () => {
    setup("Healthy", [cert({})], [issuer({})]);
    const { getByPlaceholderText } = render(<CertsView cluster="x" />);
    act(() => { window.dispatchEvent(new KeyboardEvent("keydown", { key: "/", bubbles: true, cancelable: true })); });
    expect(document.activeElement).toBe(getByPlaceholderText("Search name, issuer, DNS, secret"));
  });

  it("lists issuer health under the issuers filter", () => {
    setup("Healthy", [cert({})], [issuer({ ready: "False", reason: "ACMEAccountRegistrationFailed" })]);
    const { getByRole } = render(<CertsView cluster="x" />);
    fireEvent.click(getByRole("button", { name: /^Issuers/ }));
    const rows = rowsText().join(" ");
    expect(rows).toContain("lab-acme-staging");
    expect(rows).toContain("ACMEAccountRegistrationFailed");
  });

  // --- AI "Explain this" ---

  it("hides the Explain affordance when no AI backend is configured", async () => {
    vi.mocked(aiAvailability).mockResolvedValueOnce({ backend: "off", ready: false });
    const c = cert({ name: "acme-demo", ready: "Failed", reason: "DoesNotExist" });
    setup("Healthy", [c], [issuer({})], "payments/acme-demo");
    const { getByTestId, queryByRole } = render(<CertsView cluster="x" />);
    await act(async () => { await Promise.resolve(); });
    // The inspector renders, but no Explain button.
    expect(getByTestId("lens-inspector")).toBeTruthy();
    expect(queryByRole("button", { name: /explain/i })).toBeNull();
  });

  it("shows Explain and renders a grounded, disclaimed result on click", async () => {
    vi.mocked(aiAvailability).mockResolvedValueOnce({ backend: "hosted", ready: true });
    vi.mocked(aiEgressAllowed).mockResolvedValueOnce(true);
    vi.mocked(explain).mockResolvedValueOnce({
      ok: true, backend: "hosted", model: "claude-sonnet-5",
      groundedOn: "Certificate/payments/acme-demo, ClusterIssuer/lab-acme-staging",
      explanation: "The bound ClusterIssuer is healthy; the Challenge is stuck on a DNS record.",
      error: "",
    });
    const c = cert({ name: "acme-demo", ready: "Failed", reason: "DoesNotExist", uid: "cert-uid" });
    setup("Healthy", [c], [issuer({})], "payments/acme-demo");
    const { findByRole, findByText, getByText } = render(<CertsView cluster="x" />);

    const btn = await findByRole("button", { name: /explain/i });
    fireEvent.click(btn);

    expect(await findByText(/Challenge is stuck on a DNS record/)).toBeTruthy();
    expect(getByText(/Grounded on:/)).toBeTruthy();
    expect(getByText(/verify before acting/)).toBeTruthy();
    // The shared, UID-fenced path anchored on the certificate's row.
    expect(vi.mocked(explain)).toHaveBeenCalledWith("x", { kind: "row", task: "certs", part: "certificates", uid: "cert-uid" });
  });

  it("disables Explain when hosted egress is refused on a protected cluster", async () => {
    vi.mocked(aiAvailability).mockResolvedValueOnce({ backend: "hosted", ready: true });
    vi.mocked(aiEgressAllowed).mockResolvedValueOnce(false);
    const c = cert({ name: "acme-demo", ready: "Failed", reason: "DoesNotExist", uid: "cert-uid" });
    setup("Healthy", [c], [issuer({})], "payments/acme-demo");
    const { findByRole, queryByRole } = render(<CertsView cluster="x" />);
    // no live Explain action; the blocked reason is stated in More
    fireEvent.click(await findByRole("button", { name: /^More/ }));
    const item = (await findByRole("menuitem", { name: "Explain with AI" })) as HTMLButtonElement;
    expect(item.disabled).toBe(true);
    expect(item.title).toMatch(/protected cluster/i);
    expect(queryByRole("button", { name: /^Explain$/ })).toBeNull();
  });
});

describe("CertsView workspace template (redesign step 4c)", () => {
  const nowS = Math.floor(Date.now() / 1000);

  it("auto-selects the first certificate needing attention once, from a fresh read only", () => {
    setup("Healthy", [cert({ name: "ok" }), cert({ name: "bad", ready: "Failed" })], [issuer({})]);
    render(<CertsView cluster="x" />);
    expect(useFleet.getState().certmanager.selectedKey).toBeNull(); // cached rows at entry
    act(() => useFleet.setState((s) => ({ certmanager: { ...s.certmanager, certificates: [cert({ name: "ok" }), cert({ name: "bad", ready: "Failed" })] } })));
    expect(useFleet.getState().certmanager.selectedKey).toBe("payments/bad");
  });

  it("the renewal window is automatic renewal, not attention", () => {
    setup("Healthy", [cert({ name: "renewing", ready: "Ready", notAfterUnix: nowS + 5 * 86400, renewalUnix: nowS - 3600 })], [issuer({})]);
    const { getByText, queryByText } = render(<CertsView cluster="x" />);
    expect(getByText("all ready")).toBeTruthy();
    expect(getByText(/1 in renewal window/)).toBeTruthy();
    expect(queryByText(/not ready or expired/)).toBeNull();
  });

  it("a removed certificate keeps its last chain as read-only evidence", () => {
    const c = cert({ name: "gone", ready: "Failed" });
    const chain: CertChainDTO = { certificate: c, issuer: issuer({}), warnings: [], failingLink: null,
      requests: [{ kind: "CertificateRequest", namespace: "payments", name: "gone-1", ready: "Failed", reason: "Denied", message: "", children: [] }] };
    setup("Healthy", [c, cert({ name: "other" })], [issuer({})], "payments/gone", chain);
    const { getByText, queryByRole } = render(<CertsView cluster="x" />);
    act(() => useFleet.setState((s) => ({ certmanager: { ...s.certmanager, certificates: [cert({ name: "other" })] } })));
    expect(getByText("Certificate removed.")).toBeTruthy();
    expect(insp().getByText("gone-1")).toBeTruthy();
    expect(queryByRole("button", { name: /Re-resolve chain/ })).toBeNull();
  });

  it("a chain recorded for another cluster is never shown", () => {
    const c = cert({ name: "acme-demo" });
    const chain: CertChainDTO = { certificate: c, issuer: issuer({}), warnings: [], failingLink: null,
      requests: [{ kind: "CertificateRequest", namespace: "payments", name: "from-prd", ready: "Ready", reason: "", message: "", children: [] }] };
    setup("Healthy", [c], [issuer({})], "payments/acme-demo", chain);
    useFleet.setState((s) => ({ certmanager: { ...s.certmanager, chainCluster: "prd" } }));
    render(<CertsView cluster="x" />);
    expect(insp().queryByText("from-prd")).toBeNull();
  });

  it("an Issuer serves only its own namespace; a ClusterIssuer serves all (namespace-safe Used by)", () => {
    setup("Healthy", [
      cert({ namespace: "a", name: "in-a", issuerKind: "Issuer", issuerName: "local" }),
      cert({ namespace: "b", name: "in-b", issuerKind: "Issuer", issuerName: "local" }),
    ], [issuer({ kind: "Issuer", namespace: "a", name: "local" })]);
    const { getByRole } = render(<CertsView cluster="x" />);
    fireEvent.click(getByRole("button", { name: /^Issuers/ }));
    fireEvent.click(screen.getAllByText("local")[0]);
    expect(insp().getByText("Certificates using it · 1")).toBeTruthy();
    expect(insp().getByText("a/in-a")).toBeTruthy();
    expect(insp().queryByText("b/in-b")).toBeNull();
  });

  it("before the watch has delivered, issuer health is unverified, never 'not found'", () => {
    const c = cert({ name: "acme-demo", ready: "Failed" });
    setup("Healthy", [c], [], "payments/acme-demo");
    useFleet.setState((s) => ({ certmanager: { ...s.certmanager, loading: true } }));
    render(<CertsView cluster="x" />);
    expect(insp().getByText(/unverified \(ClusterIssuer read unavailable\)/)).toBeTruthy();
    expect(insp().queryByText(/not found in the watched issuer list/)).toBeNull();
  });
});

describe("CertsView Codex review corrections (cert-manager)", () => {
  const cov = (over: Record<string, Partial<{ served: boolean; synced: boolean; error: string }>>) =>
    ["Certificate", "Issuer", "ClusterIssuer"].map((kind) => ({ kind, served: true, synced: true, ...(over[kind] ?? {}) }));
  const setCov = (c: ReturnType<typeof cov>) => useFleet.setState((s) => ({ certmanager: { ...s.certmanager, coverage: c } }));
  const nowS = Math.floor(Date.now() / 1000);

  it("1a. Certificates synced, ClusterIssuer forbidden: issuer health unverified, no issuer-problem claim; recovery restores it", () => {
    const c = cert({ name: "acme-demo", ready: "Failed" });
    setup("Healthy", [c], [], "payments/acme-demo");
    setCov(cov({ ClusterIssuer: { synced: false, error: "forbidden" } }));
    const { queryByRole } = render(<CertsView cluster="x" />);
    expect(insp().getByText(/unverified \(ClusterIssuer read unavailable\)/)).toBeTruthy();
    expect(insp().queryByText(/not found/)).toBeNull();
    expect(queryByRole("button", { name: /^Issuer problem/ })).toBeNull();
    act(() => { useFleet.setState((s) => ({ certmanager: { ...s.certmanager, issuers: [issuer({ ready: "False", reason: "ErrRegister" })] } })); setCov(cov({})); });
    expect(insp().getByText(/Issuer not ready: ErrRegister/)).toBeTruthy();
  });

  it("1b. ClusterIssuer after-sync failure: the found issuer is marked last observed", () => {
    const c = cert({ name: "acme-demo", ready: "Ready" });
    setup("Healthy", [c], [issuer({})], "payments/acme-demo");
    setCov(cov({ ClusterIssuer: { error: "forbidden" } }));
    render(<CertsView cluster="x" />);
    expect(insp().getByText(/Ready \(last observed\)/)).toBeTruthy();
  });

  it("1c. Issuers synced, Certificates unsynced: issuer usage is unverified, never zero", () => {
    setup("Healthy", [], [issuer({ ready: "False" })]);
    setCov(cov({ Certificate: { synced: false, error: "forbidden" } }));
    const { getByRole } = render(<CertsView cluster="x" />);
    fireEvent.click(getByRole("button", { name: /^Issuers/ }));
    expect(rowsText().join(" ")).toContain("?");
    fireEvent.click(screen.getAllByText("lab-acme-staging")[0]);
    expect(insp().getByText(/usage unverified, not zero/)).toBeTruthy();
    expect(insp().queryByText(/No Certificate references this issuer/)).toBeNull();
  });

  it("2. an expired certificate is Expired even while its spec is unobserved, in row and inspector", () => {
    const c = cert({ name: "old", ready: "Ready", stale: true, generation: 4, observedGeneration: 3, notAfterUnix: nowS - 3600 });
    setup("Healthy", [c], [issuer({})], "payments/old");
    render(<CertsView cluster="x" />);
    const row = rowsText().join(" ");
    expect(row).toContain("Expired");
    expect(document.querySelector('.lens-row [data-aspect="fault"]')).toBeTruthy(); // the mark carries the shape
    expect(row).not.toContain("Syncing");
    expect(insp().getByText("Expired")).toBeTruthy();
    expect(insp().getByText(/Spec generation 4 not yet observed/)).toBeTruthy();
  });

  it("3. issuer and certificate roles agree across row and inspector", () => {
    // Unknown bound issuer: muted and unverified, never a red root-cause card
    setup("Healthy", [cert({ name: "u", ready: "Unknown" })], [issuer({ ready: "Unknown" })], "payments/u");
    const first = render(<CertsView cluster="x" />);
    expect(insp().queryByText(/Issuer failed|Issuer not ready/)).toBeNull();
    expect((insp().getByText(/Unknown \(unverified\)/) as HTMLElement).style.color).toBe("var(--color-text-tertiary)");
    expect(screen.getByRole("img", { name: "Unknown" })).toBeTruthy(); // the Unknown certificate's row lamp
    first.unmount();
    // degraded issuer: amber card; degraded certificate: amber headline
    setup("Healthy", [cert({ name: "d", ready: "Pending", message: "waiting on request" })], [issuer({ ready: "Pending", reason: "Verifying" })], "payments/d");
    render(<CertsView cluster="x" />);
    // the issuer's panel carries its tone on the panel and its mark (slice 13c.7)
    const card = (insp().getByText(/Issuer not ready: Verifying/) as HTMLElement).closest(".evidence-panel") as HTMLElement;
    expect(card.getAttribute("data-tone")).toBe("warning");
    expect(card.querySelector('[data-aspect="attention"]')).toBeTruthy();
    // the controller's words are ink-2; the tone is the verdict's mark (slice 13c.2)
    expect((insp().getByText("waiting on request") as HTMLElement).style.color).toBe("var(--color-text-secondary)");
    expect(screen.getAllByRole("img", { name: "Pending" }).length).toBeGreaterThan(0);
  });
});

describe("CertsView Unknown issuer is never a diagnosed cause (Codex re-review)", () => {
  const cov = (over: Record<string, Partial<{ served: boolean; synced: boolean; error: string }>>) =>
    ["Certificate", "Issuer", "ClusterIssuer"].map((kind) => ({ kind, served: true, synced: true, ...(over[kind] ?? {}) }));

  it("an Unknown issuer produces neither a failure card nor an issuer-derived likely cause", () => {
    setup("Healthy", [cert({ name: "u", ready: "Failed", reason: "Pending" })], [issuer({ ready: "Unknown" })], "payments/u");
    render(<CertsView cluster="x" />);
    expect(insp().queryByText(/Issuer failed|Issuer not ready/)).toBeNull();
    expect(insp().queryByText(/lab-acme-staging is not ready/)).toBeNull();
  });

  it("a chain failure still provides the cause when the issuer is Unknown", () => {
    const c = cert({ name: "u", ready: "Failed", reason: "Pending" });
    const chain: CertChainDTO = { certificate: c, issuer: issuer({ ready: "Unknown" }), warnings: [],
      requests: [{ kind: "CertificateRequest", namespace: "payments", name: "u-1", ready: "Failed", reason: "Denied", message: "", children: [] }],
      failingLink: { kind: "CertificateRequest", namespace: "payments", name: "u-1", ready: "Failed", reason: "Denied", message: "", children: [] } };
    setup("Healthy", [c], [issuer({ ready: "Unknown" })], "payments/u", chain);
    render(<CertsView cluster="x" />);
    expect(insp().getByText(/request u-1 is failed: Denied/)).toBeTruthy();
  });

  it("a real failed issuer still headlines; from a failing watch it is explicitly last observed", () => {
    setup("Healthy", [cert({ name: "f", ready: "Failed" })], [issuer({ ready: "Failed", reason: "ErrRegister" })], "payments/f");
    useFleet.setState((s) => ({ certmanager: { ...s.certmanager, coverage: cov({ ClusterIssuer: { error: "forbidden" } }) } }));
    render(<CertsView cluster="x" />);
    expect(insp().getByText(/Issuer failed \(last observed - the issuer watch is failing\): ErrRegister/)).toBeTruthy();
  });
});

describe("CertsView per-kind dataset + selection lifecycle (Codex c9231c13)", () => {
  const cov = (over: Record<string, Partial<{ served: boolean; synced: boolean; error: string }>>) =>
    ["Certificate", "Issuer", "ClusterIssuer"].map((kind) => ({ kind, served: true, synced: true, ...(over[kind] ?? {}) }));
  const setCov = (c: ReturnType<typeof cov>) => useFleet.setState((s) => ({ certmanager: { ...s.certmanager, coverage: c } }));
  const emit = (certificates: CertificateDTO[], c: ReturnType<typeof cov>) =>
    act(() => useFleet.setState((s) => ({ certmanager: { ...s.certmanager, certificates, coverage: c } })));
  const issuersTab = () => fireEvent.click(screen.getByRole("button", { name: /^Issuers/ }));

  it("an error-free pending Certificate read is loading, never proven empty; auto-select waits for the complete list", () => {
    setup("Healthy", [], [issuer({})]);
    setCov(cov({ Certificate: { synced: false } }));
    render(<CertsView cluster="x" />);
    expect(screen.queryByText("No Certificates on this cluster.")).toBeNull();
    emit([cert({ name: "bad", ready: "Failed" })], cov({ Certificate: { synced: false } }));
    expect(useFleet.getState().certmanager.selectedKey).toBeNull();
    expect(screen.getByText(/Still reading - this Certificate list is incomplete/)).toBeTruthy();
    emit([cert({ name: "bad", ready: "Failed" })], cov({}));
    expect(useFleet.getState().certmanager.selectedKey).toBe("payments/bad");
  });

  it("one issuer kind pending while the other is loaded: a lower bound, never all-ready", () => {
    setup("Healthy", [], [issuer({ kind: "Issuer", namespace: "a", name: "ca" })]);
    setCov(cov({ ClusterIssuer: { synced: false } }));
    render(<CertsView cluster="x" />);
    issuersTab();
    expect(screen.getByText("1+ issuers")).toBeTruthy();
    expect(screen.getByText("issuer list incomplete")).toBeTruthy();
    expect(screen.queryByText("all issuers ready")).toBeNull();
  });

  it("after-sync issuer failure with only Ready cached rows is last observed, never an unqualified all-ready", () => {
    setup("Healthy", [], [issuer({})]);
    setCov(cov({ ClusterIssuer: { error: "forbidden" } }));
    render(<CertsView cluster="x" />);
    issuersTab();
    expect(screen.getByText("none known not ready (last observed)")).toBeTruthy();
    expect(screen.queryByText("all issuers ready")).toBeNull();
  });

  it("a selected object is removed only when its own kind's read is complete", () => {
    setup("Healthy", [cert({ name: "gone", ready: "Failed" }), cert({ name: "other" })], [issuer({}), issuer({ kind: "Issuer", namespace: "a", name: "ca" })], "payments/gone");
    setCov(cov({}));
    render(<CertsView cluster="x" />);
    emit([cert({ name: "other" })], cov({ Certificate: { synced: false } }));
    expect(screen.queryByText("Certificate removed.")).toBeNull();
    emit([cert({ name: "other" })], cov({}));
    expect(insp().getByText("Certificate removed.")).toBeTruthy();

    issuersTab();
    fireEvent.click(screen.getAllByText("lab-acme-staging")[0]);
    // the ClusterIssuer vanishes while ITS kind is pending; the Issuer kind being complete never vouches
    act(() => useFleet.setState((s) => ({ certmanager: { ...s.certmanager, issuers: [issuer({ kind: "Issuer", namespace: "a", name: "ca" })], coverage: cov({ ClusterIssuer: { synced: false } }) } })));
    expect(screen.queryByText(/removed\./)).toBeNull();
    act(() => setCov(cov({})));
    expect(insp().getByText(/removed\./)).toBeTruthy();
  });
});

describe("CertsView on the shared rows (1c slice 3)", () => {
  const chainFor = (uid: string, warning: string): CertChainDTO => ({
    certificate: { ...cert({}), uid }, issuer: null, requests: [], failingLink: null, warnings: [warning],
  });

  it("a chain for a same-name recreation is never the selected row's detail", () => {
    setup("Healthy", [cert({ uid: "u2" })], [issuer({})], "payments/acme-demo", chainFor("u1", "chain-of-the-old-object"));
    const first = render(<CertsView cluster="x" />);
    expect(screen.queryByText(/chain-of-the-old-object/)).toBeNull();
    first.unmount();
    setup("Healthy", [cert({ uid: "u2" })], [issuer({})], "payments/acme-demo", chainFor("u2", "chain-of-this-object"));
    render(<CertsView cluster="x" />);
    expect(screen.getAllByText(/chain-of-this-object/).length).toBeGreaterThan(0);
  });

  it("past the A4 deadline the rows are qualified as of their capture", () => {
    setup("Healthy", [cert({})], [issuer({})]);
    useFleet.setState((s) => ({ certmanager: { ...s.certmanager, rowsState: { updating: false, refreshing: true, evaluatedAt: "2026-10-11T12:00:00Z" } } }));
    render(<CertsView cluster="x" />);
    expect(screen.getByTestId("rows-refreshing").textContent).toMatch(/refreshing; the totals are the cluster summary's/);
  });
});
