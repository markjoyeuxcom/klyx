import { describe, it, expect, vi } from "vitest";
import { render, fireEvent, within } from "@testing-library/react";
import { useFleet } from "../store/fleet";
import type { ExternalSecretDTO, ExternalSecretStoreDTO, ClusterDTO } from "../store/fleet";
import { ExternalSecretsView } from "./ExternalSecretsView";

vi.mock("../bridge/externalsecrets", () => ({
  openExternalSecrets: async () => () => {},
  closeExternalSecrets: async () => {},
}));

const cluster = (tier: string): ClusterDTO => ({
  name: "x", state: "Synced", reason: "", nodesReady: 1, nodesTotal: 1, pods: 1, version: "v1",
  gitopsTier: "Absent", gitopsReason: "", networkTier: "Absent", networkReason: "",
  externalSecretsTier: tier, env: "", region: "", provider: "", group: "", ageSeconds: 0,
});

const secret = (over: Partial<ExternalSecretDTO>): ExternalSecretDTO => ({
  namespace: "payments", name: "stripe-api", state: "Synced", reason: "", message: "",
  storeKind: "ClusterSecretStore", storeName: "azure-kv", targetSecret: "stripe-api-secret",
  refreshIntervalSeconds: 3600, lastRefreshAgeSeconds: 60, ...over,
});

const store = (over: Partial<ExternalSecretStoreDTO>): ExternalSecretStoreDTO => ({
  kind: "ClusterSecretStore", namespace: "", name: "azure-kv", state: "Synced", reason: "", message: "", provider: "azurekv", ...over,
});

function setup(tier: string, secrets: ExternalSecretDTO[], stores: ExternalSecretStoreDTO[], selectedKey: string | null = null, watchError: string | null = null) {
  useFleet.setState({
    clusters: [cluster(tier)],
    route: { name: "cluster", cluster: "x", section: "externalsecrets" },
    externalsecrets: { cluster: "x", secrets, stores, loading: false, watchError, selectedKey },
  });
}

describe("ExternalSecretsView", () => {
  it("renders an honest empty state when the operator is absent", () => {
    setup("Absent", [], []);
    const { getByText } = render(<ExternalSecretsView cluster="x" />);
    expect(getByText(/External Secrets Operator not detected/i)).toBeTruthy();
  });

  it("j then Enter opens the inspector for the second row (keyboard parity with Certs)", () => {
    setup("Healthy", [
      secret({ name: "broken-secret", state: "Failed", reason: "SecretSyncedError" }),
      secret({ name: "healthy-secret", state: "Synced" }),
    ], [store({})]);
    const { getByTestId } = render(<ExternalSecretsView cluster="x" />);
    fireEvent.keyDown(window, { key: "j" }); // selects first
    fireEvent.keyDown(window, { key: "j" }); // moves to second
    fireEvent.keyDown(window, { key: "Enter" });
    expect(useFleet.getState().externalsecrets.selectedKey).toBe("payments/healthy-secret");
    // Esc closes the inspector.
    fireEvent.keyDown(window, { key: "Escape" });
    expect(useFleet.getState().externalsecrets.selectedKey).toBeNull();
    expect(getByTestId("eso-scroll")).toBeTruthy();
  });

  it("sorts failing secrets first, then stale, then unknown (broken-first triage)", () => {
    setup("Healthy", [
      secret({ name: "healthy-secret", state: "Synced" }),
      secret({ name: "unknown-secret", state: "Unknown" }),
      secret({ name: "stale-secret", state: "Synced", stale: true, lastRefreshAgeSeconds: 3 * 3600 }),
      secret({ name: "broken-secret", state: "Failed", reason: "SecretSyncedError" }),
    ], [store({})]);
    const { getByTestId } = render(<ExternalSecretsView cluster="x" />);
    const rows = within(getByTestId("eso-scroll")).getAllByRole("button");
    expect(rows[0].textContent).toContain("broken-secret");
    expect(rows[1].textContent).toContain("stale-secret");
    expect(rows[2].textContent).toContain("unknown-secret");
    expect(rows[3].textContent).toContain("healthy-secret");
  });

  it("a stale row shows the amber stale chip instead of a green synced state", () => {
    setup("Healthy", [secret({ name: "stale-secret", state: "Synced", stale: true, lastRefreshAgeSeconds: 3 * 3600 })], [store({})]);
    const { getByTestId, getByTitle } = render(<ExternalSecretsView cluster="x" />);
    const row = within(getByTestId("eso-scroll")).getAllByRole("button")[0];
    expect(within(row).getByText("stale")).toBeTruthy();
    expect(row.textContent).not.toContain("synced");
    expect(getByTitle(/last synced 3h ago - refresh interval 1h; the controller has stopped refreshing/)).toBeTruthy();
  });

  it("headlines a failing bound store in the inspector", () => {
    const e = secret({ name: "stripe-api", state: "Failed", reason: "SecretSyncedError" });
    setup("Healthy", [e], [store({ state: "Failed", reason: "InvalidProviderConfig" })], "payments/stripe-api");
    const { getByTestId } = render(<ExternalSecretsView cluster="x" />);
    const inspector = getByTestId("eso-inspector");
    expect(within(inspector).getByText(/store not ready: InvalidProviderConfig/i)).toBeTruthy();
    expect(within(inspector).getByText(/likely cause/i)).toBeTruthy();
  });

  it("treats a missing store as the failure cause", () => {
    const e = secret({ name: "stripe-api", state: "Failed", reason: "SecretSyncedError", storeName: "missing-store" });
    setup("Healthy", [e], [store({})], "payments/stripe-api");
    const { getByTestId } = render(<ExternalSecretsView cluster="x" />);
    const inspector = getByTestId("eso-inspector");
    expect(within(inspector).getByText(/was not found in the watched store list/i)).toBeTruthy();
    expect(within(inspector).getByText("not found")).toBeTruthy();
  });

  it("a SecretStore only matches within the secret's namespace", () => {
    const e = secret({ name: "vault-secret", namespace: "payments", state: "Synced", storeKind: "SecretStore", storeName: "team-vault" });
    // Same-name SecretStore lives in another namespace: must NOT resolve.
    setup("Healthy", [e], [store({ kind: "SecretStore", namespace: "other", name: "team-vault", state: "Synced" })], "payments/vault-secret");
    const { getByTestId } = render(<ExternalSecretsView cluster="x" />);
    const inspector = getByTestId("eso-inspector");
    expect(within(inspector).getByText("not found")).toBeTruthy();
  });

  it("shows the watch-error banner instead of a clean empty list", () => {
    setup("Healthy", [], [], null, "externalsecrets.external-secrets.io is forbidden");
    const { getByText } = render(<ExternalSecretsView cluster="x" />);
    expect(getByText(/external-secrets watch is failing/i)).toBeTruthy();
    expect(getByText(/forbidden/)).toBeTruthy();
  });

  it("filters secrets by stale and namespace", () => {
    setup("Healthy", [
      secret({ namespace: "payments", name: "stale-secret", state: "Synced", stale: true, lastRefreshAgeSeconds: 3 * 3600 }),
      secret({ namespace: "cnpg-system", name: "fresh-secret", state: "Synced" }),
    ], [store({})]);
    const { getByRole, getByTestId, queryByText } = render(<ExternalSecretsView cluster="x" />);
    fireEvent.click(getByRole("button", { name: /^stale/i }));
    expect(within(getByTestId("eso-scroll")).getByText("stale-secret")).toBeTruthy();
    expect(queryByText("fresh-secret")).toBeNull();

    fireEvent.click(getByRole("button", { name: /^all/i }));
    fireEvent.change(getByRole("combobox", { name: /filter external secret namespace/i }), { target: { value: "cnpg-system" } });
    expect(within(getByTestId("eso-scroll")).getByText("fresh-secret")).toBeTruthy();
    expect(queryByText("stale-secret")).toBeNull();
  });

  it("lists store health with providers under the stores filter", () => {
    setup("Healthy", [secret({})], [store({ state: "Failed", reason: "InvalidProviderConfig" })]);
    const { getByRole, getByTestId } = render(<ExternalSecretsView cluster="x" />);
    fireEvent.click(getByRole("button", { name: /stores/i }));
    const panel = getByTestId("store-scroll");
    expect(within(panel).getByText("azure-kv")).toBeTruthy();
    expect(within(panel).getByText("azurekv")).toBeTruthy();
    expect(within(panel).getByText("InvalidProviderConfig")).toBeTruthy();
  });

  it("shows refresh interval and last refresh age in the inspector", () => {
    const e = secret({ name: "stripe-api", state: "Synced", refreshIntervalSeconds: 3600, lastRefreshAgeSeconds: 120 });
    setup("Healthy", [e], [store({})], "payments/stripe-api");
    const { getByTestId } = render(<ExternalSecretsView cluster="x" />);
    const inspector = getByTestId("eso-inspector");
    expect(within(inspector).getByText("1h")).toBeTruthy();
    expect(within(inspector).getByText("2m ago")).toBeTruthy();
    expect(within(inspector).getByText("stripe-api-secret")).toBeTruthy();
    expect(within(inspector).getByText("azurekv")).toBeTruthy();
  });
});
