import { describe, it, expect, vi } from "vitest";
import { render, fireEvent, within } from "@testing-library/react";
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
  dnsNames: ["acme-demo.klustr.io"], notAfterUnix: 0, expiresInSeconds: 0, renewalUnix: 0, ...over,
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
