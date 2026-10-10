import { describe, it, expect, vi } from "vitest";
import { render, fireEvent, within, screen, act } from "@testing-library/react";
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

const insp = () => within(screen.getByTestId("lens-inspector"));
const rowsText = () => [...document.querySelectorAll(".lens-row")].map((r) => r.textContent ?? "");

describe("ExternalSecretsView", () => {
  it("renders an honest empty state when the operator is absent", () => {
    setup("Absent", [], []);
    const { getByText } = render(<ExternalSecretsView cluster="x" />);
    expect(getByText(/External Secrets Operator is not installed on this cluster/i)).toBeTruthy();
  });

  it("j then Enter opens the inspector for the second row (keyboard parity with Certs)", () => {
    setup("Healthy", [
      secret({ name: "broken-secret", state: "Failed", reason: "SecretSyncedError" }),
      secret({ name: "healthy-secret", state: "Synced" }),
    ], [store({})]);
    const { container } = render(<ExternalSecretsView cluster="x" />);
    fireEvent.keyDown(window, { key: "j" }); // selects first
    fireEvent.keyDown(window, { key: "j" }); // moves to second
    fireEvent.keyDown(window, { key: "Enter" });
    expect(useFleet.getState().externalsecrets.selectedKey).toBe("payments/healthy-secret");
    // Esc closes the inspector.
    fireEvent.keyDown(window, { key: "Escape" });
    expect(useFleet.getState().externalsecrets.selectedKey).toBeNull();
    expect(container.querySelector(".lens-table")).toBeTruthy();
  });

  it("sorts failing secrets first, then stale, then unknown (broken-first triage)", () => {
    setup("Healthy", [
      secret({ name: "healthy-secret", state: "Synced" }),
      secret({ name: "unknown-secret", state: "Unknown" }),
      secret({ name: "stale-secret", state: "Synced", stale: true, lastRefreshAgeSeconds: 3 * 3600 }),
      secret({ name: "broken-secret", state: "Failed", reason: "SecretSyncedError" }),
    ], [store({})]);
    render(<ExternalSecretsView cluster="x" />);
    const rows = rowsText();
    expect(rows[0]).toContain("broken-secret");
    expect(rows[1]).toContain("stale-secret");
    expect(rows[2]).toContain("unknown-secret");
    expect(rows[3]).toContain("healthy-secret");
  });

  it("a stale row reads Stale (amber), never confidently Synced; the inspector says why", () => {
    setup("Healthy", [secret({ name: "stale-secret", state: "Synced", stale: true, lastRefreshAgeSeconds: 3 * 3600 })], [store({})], "payments/stale-secret");
    render(<ExternalSecretsView cluster="x" />);
    const row = rowsText()[0];
    expect(row).toContain("Stale");
    expect(row).not.toContain("Synced");
    expect(insp().getByText(/last synced 3h ago - refresh interval 1h; the controller has stopped refreshing/)).toBeTruthy();
  });

  it("headlines a failing bound store in the inspector", () => {
    const e = secret({ name: "stripe-api", state: "Failed", reason: "SecretSyncedError" });
    setup("Healthy", [e], [store({ state: "Failed", reason: "InvalidProviderConfig" })], "payments/stripe-api");
    render(<ExternalSecretsView cluster="x" />);
    // the failed store IS the cause - no duplicate "likely cause" box
    expect(insp().getByText(/Store failed: InvalidProviderConfig/)).toBeTruthy();
    expect(insp().queryByText(/likely cause/i)).toBeNull();
  });

  it("treats a missing store as the failure cause", () => {
    const e = secret({ name: "stripe-api", state: "Failed", reason: "SecretSyncedError", storeName: "missing-store" });
    setup("Healthy", [e], [store({})], "payments/stripe-api");
    render(<ExternalSecretsView cluster="x" />);
    expect(insp().getByText(/ClusterSecretStore\/missing-store was not found in the watched ClusterSecretStore list/)).toBeTruthy();
    expect(insp().getByText("not found in the watched ClusterSecretStore list")).toBeTruthy();
  });

  it("a SecretStore only matches within the secret's namespace", () => {
    const e = secret({ name: "vault-secret", namespace: "payments", state: "Synced", storeKind: "SecretStore", storeName: "team-vault" });
    // Same-name SecretStore lives in another namespace: must NOT resolve.
    setup("Healthy", [e], [store({ kind: "SecretStore", namespace: "other", name: "team-vault", state: "Synced" })], "payments/vault-secret");
    render(<ExternalSecretsView cluster="x" />);
    expect(insp().getByText("not found in the watched SecretStore list")).toBeTruthy();
  });

  it("shows the watch-error banner instead of a clean empty list", () => {
    setup("Healthy", [], [], null, "externalsecrets.external-secrets.io is forbidden");
    const { getByText } = render(<ExternalSecretsView cluster="x" />);
    expect(getByText("External Secrets state could not be read.")).toBeTruthy();
    expect(getByText(/forbidden/)).toBeTruthy();
  });

  it("filters secrets by stale and namespace", () => {
    setup("Healthy", [
      secret({ namespace: "payments", name: "stale-secret", state: "Synced", stale: true, lastRefreshAgeSeconds: 3 * 3600 }),
      secret({ namespace: "cnpg-system", name: "fresh-secret", state: "Synced" }),
    ], [store({})]);
    const { getByRole, getByLabelText, queryByText } = render(<ExternalSecretsView cluster="x" />);
    fireEvent.click(getByRole("button", { name: /^Stale \d+$/ }));
    expect(rowsText().join(" ")).toContain("stale-secret");
    expect(queryByText("fresh-secret")).toBeNull();

    fireEvent.click(getByRole("button", { name: /^All$/ }));
    fireEvent.change(getByLabelText("Namespace"), { target: { value: "cnpg-system" } });
    expect(rowsText().join(" ")).toContain("fresh-secret");
    expect(queryByText("stale-secret")).toBeNull();
  });

  it("lists store health with providers under the stores filter", () => {
    setup("Healthy", [secret({})], [store({ state: "Failed", reason: "InvalidProviderConfig" })]);
    const { getByRole } = render(<ExternalSecretsView cluster="x" />);
    fireEvent.click(getByRole("button", { name: /^Stores/ }));
    const rows = rowsText().join(" ");
    expect(rows).toContain("azure-kv");
    expect(rows).toContain("azurekv");
    expect(rows).toContain("InvalidProviderConfig");
  });

  it("shows refresh interval and last refresh age in the inspector", () => {
    const e = secret({ name: "stripe-api", state: "Synced", refreshIntervalSeconds: 3600, lastRefreshAgeSeconds: 120 });
    setup("Healthy", [e], [store({})], "payments/stripe-api");
    render(<ExternalSecretsView cluster="x" />);
    expect(insp().getByText("1h")).toBeTruthy();
    expect(insp().getByText("2m ago")).toBeTruthy();
    expect(insp().getByText(/Secret stripe-api-secret/)).toBeTruthy();
    expect(insp().getByText("azurekv")).toBeTruthy();
    expect(insp().getByRole("button", { name: "Open target Secret" })).toBeTruthy();
  });
});

describe("ExternalSecretsView workspace template + coverage (redesign step 4d)", () => {
  const cov = (over: Record<string, Partial<{ served: boolean; synced: boolean; error: string }>>) =>
    ["ExternalSecret", "SecretStore", "ClusterSecretStore"].map((kind) => ({ kind, served: true, synced: true, ...(over[kind] ?? {}) }));
  const setCov = (c: ReturnType<typeof cov>) => useFleet.setState((s) => ({ externalsecrets: { ...s.externalsecrets, coverage: c } }));

  it("auto-selects the first secret needing attention once, from a fresh read only", () => {
    setup("Healthy", [secret({ name: "ok" }), secret({ name: "bad", state: "Failed" })], [store({})]);
    render(<ExternalSecretsView cluster="x" />);
    expect(useFleet.getState().externalsecrets.selectedKey).toBeNull();
    act(() => useFleet.setState((s) => ({ externalsecrets: { ...s.externalsecrets, secrets: [secret({ name: "ok" }), secret({ name: "bad", state: "Failed" })] } })));
    expect(useFleet.getState().externalsecrets.selectedKey).toBe("payments/bad");
  });

  it("a removed secret keeps its last evidence, read-only", () => {
    setup("Healthy", [secret({ name: "gone", state: "Failed", reason: "SecretSyncedError" }), secret({ name: "other" })], [store({})], "payments/gone");
    render(<ExternalSecretsView cluster="x" />);
    act(() => useFleet.setState((s) => ({ externalsecrets: { ...s.externalsecrets, secrets: [secret({ name: "other" })] } })));
    expect(insp().getByText("ExternalSecret removed.")).toBeTruthy();
    expect(insp().queryByRole("button", { name: "Open target Secret" })).toBeNull();
  });

  it("ExternalSecrets synced, ClusterSecretStore forbidden: store health unverified, no store-problem claim", () => {
    setup("Healthy", [secret({ name: "s", state: "Failed" })], [], "payments/s");
    setCov(cov({ ClusterSecretStore: { synced: false, error: "forbidden" } }));
    render(<ExternalSecretsView cluster="x" />);
    expect(insp().getByText(/unverified \(ClusterSecretStore read unavailable\)/)).toBeTruthy();
    expect(insp().queryByText(/not found/)).toBeNull();
    expect(screen.queryByText(/with store problem/)).toBeNull();
  });

  it("stores synced, ExternalSecrets unsynced: store usage is unverified, never zero", () => {
    setup("Healthy", [], [store({ state: "Failed" })]);
    setCov(cov({ ExternalSecret: { synced: false, error: "forbidden" } }));
    const { getByRole } = render(<ExternalSecretsView cluster="x" />);
    fireEvent.click(getByRole("button", { name: /^Stores/ }));
    expect(rowsText().join(" ")).toContain("?");
    fireEvent.click(screen.getAllByText("azure-kv")[0]);
    expect(insp().getByText(/usage unverified, not zero/)).toBeTruthy();
  });

  it("an Unknown store is neither a failure card nor a store-derived cause; a failed store from a failing watch is last observed", () => {
    setup("Healthy", [secret({ name: "u", state: "Unknown" })], [store({ state: "Unknown" })], "payments/u");
    const first = render(<ExternalSecretsView cluster="x" />);
    expect(insp().queryByText(/Store failed|Store not ready/)).toBeNull();
    expect(insp().queryByText(/likely cause/i)).toBeNull();
    expect((insp().getByText(/Unknown \(unverified\)/) as HTMLElement).style.color).toBe("var(--color-text-tertiary)");
    first.unmount();
    setup("Healthy", [secret({ name: "f", state: "Failed" })], [store({ state: "Failed", reason: "InvalidProviderConfig" })], "payments/f");
    setCov(cov({ ClusterSecretStore: { error: "forbidden" } }));
    render(<ExternalSecretsView cluster="x" />);
    expect(insp().getByText(/Store failed \(last observed - the store watch is failing\): InvalidProviderConfig/)).toBeTruthy();
  });

  it("a SecretStore serves only its own namespace (namespace-safe Used by)", () => {
    setup("Healthy", [
      secret({ namespace: "a", name: "in-a", storeKind: "SecretStore", storeName: "vault" }),
      secret({ namespace: "b", name: "in-b", storeKind: "SecretStore", storeName: "vault" }),
    ], [store({ kind: "SecretStore", namespace: "a", name: "vault" })]);
    const { getByRole } = render(<ExternalSecretsView cluster="x" />);
    fireEvent.click(getByRole("button", { name: /^Stores/ }));
    fireEvent.click(screen.getAllByText("vault")[0]);
    expect(insp().getByText("ExternalSecrets using it · 1")).toBeTruthy();
    expect(insp().getByText("a/in-a")).toBeTruthy();
    expect(insp().queryByText("b/in-b")).toBeNull();
  });
});

describe("ExternalSecretsView per-kind dataset + selection lifecycle (Codex c9231c13)", () => {
  const cov = (over: Record<string, Partial<{ served: boolean; synced: boolean; error: string }>>) =>
    ["ExternalSecret", "SecretStore", "ClusterSecretStore"].map((kind) => ({ kind, served: true, synced: true, ...(over[kind] ?? {}) }));
  const setCov = (c: ReturnType<typeof cov>) => useFleet.setState((s) => ({ externalsecrets: { ...s.externalsecrets, coverage: c } }));
  const emit = (secrets: ExternalSecretDTO[], c: ReturnType<typeof cov>) =>
    act(() => useFleet.setState((s) => ({ externalsecrets: { ...s.externalsecrets, secrets, coverage: c } })));

  it("an error-free pending ExternalSecret read is loading, never proven empty; auto-select waits for the complete list", () => {
    setup("Healthy", [], [store({})]);
    setCov(cov({ ExternalSecret: { synced: false } }));
    render(<ExternalSecretsView cluster="x" />);
    expect(screen.queryByText("No ExternalSecrets on this cluster.")).toBeNull();
    emit([secret({ name: "bad", state: "Failed" })], cov({ ExternalSecret: { synced: false } }));
    expect(useFleet.getState().externalsecrets.selectedKey).toBeNull();
    expect(screen.getByText(/Still reading - this ExternalSecret list is incomplete/)).toBeTruthy();
    emit([secret({ name: "bad", state: "Failed" })], cov({}));
    expect(useFleet.getState().externalsecrets.selectedKey).toBe("payments/bad");
  });

  it("one store kind pending while the other is loaded: a lower bound, never all-ready", () => {
    setup("Healthy", [], [store({ kind: "SecretStore", namespace: "a", name: "vault" })]);
    setCov(cov({ ClusterSecretStore: { synced: false } }));
    const { getByRole } = render(<ExternalSecretsView cluster="x" />);
    fireEvent.click(getByRole("button", { name: /^Stores/ }));
    expect(screen.getByText("1+")).toBeTruthy();
    expect(screen.getByText("store list incomplete")).toBeTruthy();
    expect(screen.queryByText("all stores ready")).toBeNull();
  });

  it("after-sync store failure with only Ready cached rows is last observed, never an unqualified all-ready", () => {
    setup("Healthy", [], [store({})], null, "clustersecretstores forbidden");
    setCov(cov({ ClusterSecretStore: { error: "forbidden" } }));
    const { getByRole } = render(<ExternalSecretsView cluster="x" />);
    fireEvent.click(getByRole("button", { name: /^Stores/ }));
    expect(screen.getByText("none known not ready (last observed)")).toBeTruthy();
    expect(screen.queryByText("all stores ready")).toBeNull();
  });

  it("a failing ExternalSecret watch does not invalidate a complete store catalogue", () => {
    setup("Healthy", [], [store({})], null, "externalsecrets forbidden");
    setCov(cov({ ExternalSecret: { synced: false, error: "forbidden" } }));
    const { getByRole } = render(<ExternalSecretsView cluster="x" />);
    fireEvent.click(getByRole("button", { name: /^Stores/ }));
    expect(screen.getByText("all stores ready")).toBeTruthy();
  });

  it("a selected object is removed only when its own kind's read is complete", () => {
    setup("Healthy", [secret({ name: "gone", state: "Failed" }), secret({ name: "other" })], [store({}), store({ kind: "SecretStore", namespace: "a", name: "vault" })], "payments/gone");
    setCov(cov({}));
    const { getByRole } = render(<ExternalSecretsView cluster="x" />);
    emit([secret({ name: "other" })], cov({ ExternalSecret: { synced: false } }));
    expect(screen.queryByText("ExternalSecret removed.")).toBeNull();
    emit([secret({ name: "other" })], cov({}));
    expect(insp().getByText("ExternalSecret removed.")).toBeTruthy();

    fireEvent.click(getByRole("button", { name: /^Stores/ }));
    fireEvent.click(screen.getAllByText("azure-kv")[0]);
    // the ClusterSecretStore vanishes while ITS kind is pending; the SecretStore kind being complete never vouches
    act(() => useFleet.setState((s) => ({ externalsecrets: { ...s.externalsecrets, stores: [store({ kind: "SecretStore", namespace: "a", name: "vault" })], coverage: cov({ ClusterSecretStore: { synced: false } }) } })));
    expect(screen.queryByText(/removed\./)).toBeNull();
    act(() => setCov(cov({})));
    expect(insp().getByText(/removed\./)).toBeTruthy();
  });
});

describe("ExternalSecretsView refresh presence (Codex optional follow-up)", () => {
  it("a sync under a second ago is not 'never synced'; an absent refreshTime is", () => {
    setup("Healthy", [secret({ name: "fresh", lastRefreshAgeSeconds: 0, refreshed: true })], [store({})], "payments/fresh");
    const first = render(<ExternalSecretsView cluster="x" />);
    expect(insp().getByText("0s ago")).toBeTruthy();
    expect(insp().queryByText("never synced")).toBeNull();
    first.unmount();
    setup("Healthy", [secret({ name: "never", state: "Failed", lastRefreshAgeSeconds: 0, refreshed: false })], [store({})], "payments/never");
    render(<ExternalSecretsView cluster="x" />);
    expect(insp().getByText("never synced")).toBeTruthy();
  });
});
