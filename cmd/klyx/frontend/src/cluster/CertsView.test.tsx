import { describe, it, expect, vi } from "vitest";
import { render, fireEvent, within, act } from "@testing-library/react";
import { useFleet } from "../store/fleet";
import type { CertificateDTO, IssuerDTO, CertChainDTO, ClusterDTO } from "../store/fleet";
import { CertsView } from "./CertsView";

vi.mock("../bridge/certmanager", () => ({
  openCertManager: async () => () => {},
  closeCertManager: async () => {},
  getCertificateChain: async () => {},
}));

const cluster = (tier: string): ClusterDTO => ({
  name: "x", state: "Synced", reason: "", nodesReady: 1, nodesTotal: 1, pods: 1, version: "v1",
  gitopsTier: "Absent", gitopsReason: "", networkTier: "Absent", networkReason: "",
  certManagerTier: tier, env: "", region: "", provider: "", group: "", ageSeconds: 0,
});

const cert = (over: Partial<CertificateDTO>): CertificateDTO => ({
  namespace: "payments", name: "acme-demo", ready: "Ready", reason: "", message: "",
  issuerKind: "ClusterIssuer", issuerName: "lab-acme-staging", secretName: "acme-demo-tls",
  dnsNames: ["acme-demo.klustr.io"], notBeforeUnix: 0, notAfterUnix: 0, expiresInSeconds: 0, renewalUnix: 0, ...over,
});

const issuer = (over: Partial<IssuerDTO>): IssuerDTO => ({
  kind: "ClusterIssuer", namespace: "", name: "lab-acme-staging", ready: "Ready", reason: "", message: "", ...over,
});

function setup(tier: string, certs: CertificateDTO[], issuers: IssuerDTO[], selectedKey: string | null = null, chain: CertChainDTO | null = null) {
  useFleet.setState({
    clusters: [cluster(tier)],
    route: { name: "cluster", cluster: "x", section: "certs" },
    certmanager: { cluster: "x", certificates: certs, issuers, loading: false, selectedKey, chain, chainLoading: false },
  });
}

describe("CertsView", () => {
  it("a stale certificate dims and shows the Syncing chip", () => {
    setup("Healthy", [cert({ name: "edited-tls", ready: "Ready", stale: true, generation: 3, observedGeneration: 2 })], [issuer({})]);
    const { getByText, getByTitle } = render(<CertsView cluster="x" />);
    const row = getByText("edited-tls").closest("button") as HTMLButtonElement;
    expect(getByText("Syncing…")).toBeTruthy();
    expect(row.textContent).not.toContain("ready");
    expect(getByTitle(/Observed Gen: 2 vs Current: 3/)).toBeTruthy();
    expect(row.style.opacity).toBe("0.6");
  });


  it("renders an honest empty state when cert-manager is absent", () => {
    setup("Absent", [], []);
    const { getByText } = render(<CertsView cluster="x" />);
    expect(getByText(/cert-manager not detected/i)).toBeTruthy();
  });

  it("sorts failing certificates first (broken-first triage)", () => {
    setup("Healthy", [
      cert({ name: "healthy-cert", ready: "Ready" }),
      cert({ name: "broken-cert", ready: "Failed", reason: "DoesNotExist" }),
    ], []);
    const { getByTestId } = render(<CertsView cluster="x" />);
    const rows = within(getByTestId("cert-scroll")).getAllByRole("button");
    expect(rows[0].textContent).toContain("broken-cert");
    expect(rows[1].textContent).toContain("healthy-cert");
  });

  it("the inspector opens with the shared verdict: state word plus reason, facts as counters", () => {
    const c = cert({ name: "acme-demo", ready: "Failed", reason: "DoesNotExist" });
    setup("Healthy", [c], [issuer({})], "payments/acme-demo");
    const { getByTestId } = render(<CertsView cluster="x" />);
    const inspector = getByTestId("cert-inspector");
    expect(within(inspector).getByText("Failed")).toBeTruthy();
    expect(within(inspector).getByText(/DoesNotExist/)).toBeTruthy();
    // The requests/warnings/stuck tiles are now InspectorFacts counter wells.
    expect(within(inspector).getByText("requests")).toBeTruthy();
    expect(within(inspector).getByText("stuck")).toBeTruthy();
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
    const { getByTestId } = render(<CertsView cluster="x" />);
    const inspector = getByTestId("cert-inspector");
    expect(within(inspector).getByText(/deepest stuck node flagged/i)).toBeTruthy();
    expect(within(inspector).getByText("propagation check failed: NXDOMAIN")).toBeTruthy();
  });

  it("headlines a failing bound issuer in the inspector", () => {
    const c = cert({ name: "acme-demo", ready: "Failed", reason: "DoesNotExist" });
    setup("Healthy", [c], [issuer({ ready: "False", reason: "ACMEAccountRegistrationFailed" })], "payments/acme-demo");
    const { getByTestId } = render(<CertsView cluster="x" />);
    const inspector = getByTestId("cert-inspector");
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
    const { getByTestId } = render(<CertsView cluster="x" />);
    const inspector = getByTestId("cert-inspector");
    expect(within(inspector).getByText("acme-demo-1-123-456")).toBeTruthy();
    expect(within(inspector).getByText(/stuck here/i)).toBeTruthy();
    // The server-resolved bound issuer drives the headline.
    expect(within(inspector).getByText(/issuer not ready: ACMEAccountRegistrationFailed/i)).toBeTruthy();
    expect(within(inspector).getByText(/likely cause/i)).toBeTruthy();
  });

  it("filters certificates by issuing, issuer problem, and namespace", () => {
    setup("Healthy", [
      cert({ namespace: "payments", name: "issuing-cert", ready: "Issuing" }),
      cert({ namespace: "cnpg-system", name: "ready-cert", ready: "Ready", issuerName: "missing-issuer" }),
    ], [issuer({})]);
    const { getAllByRole, getByRole, getByTestId, queryByText } = render(<CertsView cluster="x" />);
    fireEvent.click(getAllByRole("button", { name: /issuing/i })[0]);
    expect(within(getByTestId("cert-scroll")).getByText("issuing-cert")).toBeTruthy();
    expect(queryByText("ready-cert")).toBeNull();

    fireEvent.click(getByRole("button", { name: /issuer problem/i }));
    expect(within(getByTestId("cert-scroll")).getByText("ready-cert")).toBeTruthy();

    fireEvent.change(getByRole("combobox", { name: /filter certificate namespace/i }), { target: { value: "payments" } });
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
    const { getByTestId } = render(<CertsView cluster="x" />);
    const inspector = getByTestId("cert-inspector");
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
    const { getByTestId } = render(<CertsView cluster="x" />);
    const inspector = getByTestId("cert-inspector");
    expect(within(inspector).getByText("cnpg-system/cnpg-ca")).toBeTruthy();
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
    expect(document.activeElement).toBe(getByPlaceholderText("filter certificates"));
  });

  it("lists issuer health under the issuers filter", () => {
    setup("Healthy", [cert({})], [issuer({ ready: "False", reason: "ACMEAccountRegistrationFailed" })]);
    const { getByRole, getByTestId } = render(<CertsView cluster="x" />);
    fireEvent.click(getByRole("button", { name: /issuers/i }));
    const panel = getByTestId("issuer-scroll");
    expect(within(panel).getByText("lab-acme-staging")).toBeTruthy();
    expect(within(panel).getByText("ACMEAccountRegistrationFailed")).toBeTruthy();
  });
});
