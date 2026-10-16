import { describe, it, expect, vi } from "vitest";
import { render, fireEvent, within, screen, act } from "@testing-library/react";
import { useFleet } from "../store/fleet";
import type { ExternalSecretDTO, ExternalSecretStoreDTO, ClusterDTO } from "../store/fleet";
import { ExternalSecretsView } from "./ExternalSecretsView";
import { secretRule, storeRule, withRule } from "../test/backendRules";

// drawnBeforeActions: the element sits in the inspector's drawing slot, ahead
// of its action band (The Line slice 13c.3, parent §4).
function drawnBeforeActions(el: HTMLElement): boolean {
  const panel = el.closest('[data-testid="lens-inspector"]') ?? document.body;
  const actions = panel.querySelector('[role="group"][aria-label="actions"]');
  return !!el.closest(".insp-drawing") && !!actions && !!(el.compareDocumentPosition(actions) & Node.DOCUMENT_POSITION_FOLLOWING);
}

// The rows hook is the transport; these tests seed the externalsecrets slice it fills.
vi.mock("../health/useExternalSecretsRows", () => ({ useExternalSecretsRows: () => {} }));

const cluster = (tier: string): ClusterDTO => ({
  name: "x", state: "Synced", reason: "", nodesReady: 1, nodesTotal: 1, pods: 1, version: "v1",
  gitopsTier: "Absent", gitopsReason: "", networkTier: "Absent", networkReason: "",
  externalSecretsTier: tier, env: "", region: "", provider: "", group: "", ageSeconds: 0,
});

// Fixtures carry the rule the backend computes (test/backendRules.ts, held to
// the Go golden fixture), stamped at build time - the lens only reads it.
const secret = (over: Partial<ExternalSecretDTO>): ExternalSecretDTO => withRule<ExternalSecretDTO>({
  namespace: "payments", name: "stripe-api", state: "Synced", reason: "", message: "",
  storeKind: "ClusterSecretStore", storeName: "azure-kv", targetSecret: "stripe-api-secret",
  refreshIntervalSeconds: 3600, lastRefreshAgeSeconds: 60, ...over,
}, secretRule);

const store = (over: Partial<ExternalSecretStoreDTO>): ExternalSecretStoreDTO => withRule<ExternalSecretStoreDTO>({
  kind: "ClusterSecretStore", namespace: "", name: "azure-kv", state: "Synced", reason: "", message: "", provider: "azurekv", ...over,
}, storeRule);

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
  it("the Stores view reads the rule: attention filter, count and verdict label (Codex 2b-1 #1)", () => {
    setup("Healthy", [], [
      store({ name: "flagged", state: "Synced", rule: { included: true, role: "fault", label: "Flagged by backend" } }),
      store({ name: "cleared", state: "Failed", reason: "InvalidProviderConfig", rule: { included: false, role: "ok", label: "Ready" } }),
    ]);
    render(<ExternalSecretsView cluster="x" />);
    fireEvent.click(screen.getByRole("button", { name: /^Stores/ }));
    expect(screen.getByText("flagged").closest(".lens-row")?.textContent).toContain("Flagged by backend");
    expect(screen.getByText("cleared").closest(".lens-row")?.textContent).not.toContain("InvalidProviderConfig");
    fireEvent.click(screen.getByRole("button", { name: /^Not ready/ }));
    expect(screen.getByRole("button", { name: /^Not ready/ }).textContent).toBe("Not ready 1");
    const rows = [...document.querySelectorAll(".lens-row")].map((r) => r.textContent ?? "");
    expect(rows.map((r) => r.includes("flagged"))).toEqual([true]); // count and rows agree
  });

  it("reads the Go-computed rule, never its own: verdict, label and the attention count (1c §2)", () => {
    // The rules contradict the native fields on purpose: only the rule may decide.
    setup("Healthy", [
      secret({ name: "backend-flags", state: "Synced", rule: { included: true, role: "fault", label: "Flagged by backend" } }),
      secret({ name: "backend-clears", state: "Failed", rule: { included: false, role: "ok", label: "Synced" } }),
    ], [store({ state: "Failed", rule: { included: false, role: "ok", label: "Ready" } })]);
    render(<ExternalSecretsView cluster="x" />);
    expect(screen.getByText("backend-flags").closest(".lens-row")?.textContent).toContain("Flagged by backend");
    expect(screen.getByRole("button", { name: /^Not synced or refresh overdue/ }).textContent).toBe("Not synced or refresh overdue 1");
  });

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

  // The Line slice 8a (parent §5.1): store -> ExternalSecret -> target Secret
  // on Rail, today's evidence on its steps, nothing new claimed.
  it("draws the chain on Rail: the store failure on the store, the refresh on the ExternalSecret, then the target", () => {
    const e = secret({ name: "stripe-api", state: "Failed", reason: "SecretSyncedError" });
    setup("Healthy", [e], [store({ state: "Failed", reason: "InvalidProviderConfig" })], "payments/stripe-api");
    render(<ExternalSecretsView cluster="x" />);
    const rail = insp().getByRole("list", { name: "Store, ExternalSecret and target Secret" });
    expect(drawnBeforeActions(rail)).toBe(true);
    const steps = [...rail.querySelectorAll(":scope > li")];
    expect(steps.map((li) => li.getAttribute("data-step"))).toEqual(["store", "externalsecret", "target"]);
    expect(steps[0].querySelector('[data-aspect="fault"]')).not.toBeNull();
    expect(steps[0].textContent).toMatch(/Store failed: InvalidProviderConfig/);
    expect(steps[1].textContent).toMatch(/refresh/);
    expect(steps[2].textContent).toContain("target Secret");
    // the chain's text roles (slice 13c review): kind words apart, names in the name role, nothing sized inline
    expect([...rail.querySelectorAll(".rail-kind")].map((k) => k.textContent)).toEqual(expect.arrayContaining(["ExternalSecret", "target Secret"]));
    expect(rail.querySelectorAll('[style*="font-size"]')).toHaveLength(0);
    expect(steps[2].querySelector("[data-aspect]")).toBeNull(); // the target is not read: no state claimed
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
    expect(insp().getByText("every 1h")).toBeTruthy();
    expect(insp().getByText("2m ago")).toBeTruthy();
    expect(insp().getByText(/Secret stripe-api-secret/)).toBeTruthy();
    expect(insp().getByText("azurekv")).toBeTruthy();
    expect(insp().getByRole("button", { name: "Open target Secret" })).toBeTruthy();
  });

  it("names a non-periodic refresh policy instead of an interval (Codex review 2026-10-13)", () => {
    const once = secret({ name: "once", state: "Synced", refreshIntervalSeconds: 3600, refreshPolicy: "CreatedOnce", lastRefreshAgeSeconds: 3 * 86400 });
    setup("Healthy", [once], [store({})], "payments/once");
    const { unmount } = render(<ExternalSecretsView cluster="x" />);
    expect(insp().getByText("created once - no periodic refresh (re-synced if the target Secret is changed or deleted)")).toBeTruthy();
    expect(insp().queryByText("every 1h")).toBeNull();
    unmount();
    const onChange = secret({ name: "onchange", state: "Synced", refreshIntervalSeconds: 3600, refreshPolicy: "OnChange", lastRefreshAgeSeconds: 120 });
    setup("Healthy", [onChange], [store({})], "payments/onchange");
    render(<ExternalSecretsView cluster="x" />);
    expect(insp().getByText("on metadata or spec change - no periodic refresh")).toBeTruthy();
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
    expect(screen.getByText("1+ stores")).toBeTruthy();
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

describe("ExternalSecretsView count markers (Codex d607e454 polish)", () => {
  const cov = (over: Record<string, Partial<{ served: boolean; synced: boolean; error: string }>>) =>
    ["ExternalSecret", "SecretStore", "ClusterSecretStore"].map((kind) => ({ kind, served: true, synced: true, ...(over[kind] ?? {}) }));
  it("a stale set is marked last observed (*), a pending one a lower bound (+)", () => {
    setup("Healthy", [], [store({})], null, "forbidden");
    useFleet.setState((s) => ({ externalsecrets: { ...s.externalsecrets, coverage: cov({ ClusterSecretStore: { error: "forbidden" } }) } }));
    const { getByRole } = render(<ExternalSecretsView cluster="x" />);
    fireEvent.click(getByRole("button", { name: /^Stores/ }));
    expect(screen.getByText("1* stores")).toBeTruthy();
    expect(screen.getAllByText("ExternalSecrets").length).toBeGreaterThan(1); // view toggle + the count column names controller objects, not target Secrets
  });
});
