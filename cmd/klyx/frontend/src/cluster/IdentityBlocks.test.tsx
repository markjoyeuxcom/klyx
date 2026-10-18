import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, within, waitFor, act, fireEvent } from "@testing-library/react";
import type { IdPod, IdPodReport, IdSide, PodIdentityDTO, SAIdentityDTO, StoreIdentityDTO } from "../bridge/identity";
import { useFleet } from "../store/fleet";

let saReply: (c: string, ns: string, n: string, uid: string) => Promise<SAIdentityDTO>;
let podReply: (c: string, ns: string, n: string, uid: string) => Promise<PodIdentityDTO>;
let storeReply: (c: string, k: string, ns: string, n: string, uid: string) => Promise<StoreIdentityDTO>;
vi.mock("../bridge/identity", () => ({
  readServiceAccountIdentity: (c: string, ns: string, n: string, u: string) => saReply(c, ns, n, u),
  readPodIdentity: (c: string, ns: string, n: string, u: string) => podReply(c, ns, n, u),
  readStoreIdentity: (c: string, k: string, ns: string, n: string, u: string) => storeReply(c, k, ns, n, u),
}));
const { ServiceAccountIdentity, PodIdentity, StoreAuthenticatesAs } = await import("./IdentityBlocks");

const pod = (name: string, over: Partial<IdPod> = {}): IdPod => ({ namespace: "app", name, uid: `u-${name}`, serviceAccount: "web", node: "n1", created: "2026-10-18T08:00:00Z",
  azureOptIn: false, containers: [{ name: "app" }], ...over });
const side = (over: Partial<IdSide> = {}): IdSide => ({ component: "eks-pod-identity-agent", health: "running on n1",
  management: { manager: "unknown", managerEvidence: "the add-on's labels are not verified", observed: "observed", observedEvidence: "its pod on this node" }, ...over });
const report = (p: IdPod, readings: IdPodReport["readings"], nodes?: IdSide[]): IdPodReport => ({ pod: p, compared: readings !== null, readings, nodes });
const LIMIT = "cloud trust, associations and permissions are not visible";
const saDTO = (uid: string, over: Partial<SAIdentityDTO["report"]> = {}, dto: Partial<SAIdentityDTO> = {}): SAIdentityDTO => ({ cluster: "c", namespace: "app", name: "web", uid,
  report: { readAt: "2026-10-18T09:00:00Z", state: "read", declared: { namespace: "app", name: "web", uid, roleArn: { shown: "arn:aws:iam::123456789012:role/web" } },
    pods: [], podsRead: "complete", stores: [], storesRead: "complete", limits: [LIMIT], ...over }, ...dto });
const deferred = <T,>() => { let resolve!: (v: T) => void; const promise = new Promise<T>((r) => { resolve = r; }); return { promise, resolve }; };

describe("identity blocks (coverage S5 §5)", () => {
  beforeEach(() => {
    saReply = async (_c, _ns, _n, u) => saDTO(u);
    podReply = async (c, ns, n, u) => ({ cluster: c, namespace: ns, name: n, uid: u, report: { readAt: "2026-10-18T09:00:00Z", state: "read", saState: "read", limits: [LIMIT] } });
    storeReply = async (c, k, ns, n, u) => ({ cluster: c, kind: k, namespace: ns, name: n, uid: u,
      report: { readAt: "2026-10-18T09:00:00Z", state: "read", subject: { provider: "aws", mode: "jwt", base: "the ServiceAccount's token" }, limits: [] } });
    useFleet.setState({ route: { name: "cluster", cluster: "c", section: "resources" } as never, navIntent: null });
  });

  it("the ServiceAccount block says its declarations, each pod's reading, the node side with its management, the stores and the limits", async () => {
    saReply = async (_c, _ns, _n, u) => saDTO(u, {
      pods: [report(pod("a"), [{ adapter: "aws", container: "app", state: "differs", text: "Configured role arn:aws:iam::123456789012:role/old differs from the declared one." }]),
        report(pod("b"), [{ adapter: "aws", container: "app", state: "pod-identity", text: "Uses EKS Pod Identity." }], [side({ health: "no agent pod on n1", fault: true })])],
      podsRead: "more",
      stores: [{ kind: "SecretStore", namespace: "app", name: "aws", uid: "s1", provider: "aws", mode: "jwt", via: "serviceAccountRef" }],
    });
    render(<ServiceAccountIdentity cluster="c" namespace="app" name="web" uid="sa-1" active />);
    const b = await screen.findByTestId("identity-declared");
    expect(b.textContent).toContain("arn:aws:iam::123456789012:role/web");
    expect(b.textContent).toContain("declares nothing on a ServiceAccount");
    const block = screen.getByTestId("identity-sa");
    expect(block.textContent).toContain("count is unknown");
    expect(within(screen.getByTestId("identity-reading-differs")).getByRole("img", { name: "look at" })).toBeTruthy();
    expect(within(screen.getByTestId("identity-reading-pod-identity")).queryByRole("img")).toBeNull();
    const s = screen.getByTestId("identity-side");
    expect(within(s).getByRole("img", { name: "fault" })).toBeTruthy();
    expect(s.textContent).toContain("manager unknown: the add-on's labels are not verified");
    expect(s.textContent).toContain("observed: its pod on this node");
    expect(s.textContent?.match(/no agent pod on n1/g)?.length).toBe(1);
    expect(screen.getByTestId("identity-store").textContent).toContain("serviceAccountRef");
    expect(screen.getByTestId("identity-limits").textContent).toContain(LIMIT);
  });

  it("an unreadable pod list is said, never 'no pods'; a failed read is said, never empty", async () => {
    saReply = async (_c, _ns, _n, u) => saDTO(u, { podsRead: "unreadable" });
    const { unmount } = render(<ServiceAccountIdentity cluster="c" namespace="app" name="web" uid="sa-1" active />);
    await screen.findByTestId("identity-declared");
    expect(screen.getByTestId("identity-sa").textContent).toContain("none can be said to exist or not");
    expect(screen.getByTestId("identity-sa").textContent).not.toContain("No pods run");
    unmount();
    saReply = async (_c, _ns, _n, u) => ({ ...saDTO(u), error: "the read failed: forbidden" });
    render(<ServiceAccountIdentity cluster="c" namespace="app" name="web" uid="sa-1" active />);
    await waitFor(() => expect(screen.getByTestId("identity-sa").textContent).toContain("forbidden"));
    expect(screen.queryByTestId("identity-declared")).toBeNull();
  });

  it("a reply is shown only under the identity it was read for: a late reply for the old object never renders under the new one", async () => {
    const first = deferred<SAIdentityDTO>();
    const second = deferred<SAIdentityDTO>();
    saReply = async (_c, _ns, _n, u) => (u === "sa-1" ? first.promise : second.promise);
    const { rerender } = render(<ServiceAccountIdentity cluster="c" namespace="app" name="web" uid="sa-1" active />);
    rerender(<ServiceAccountIdentity cluster="c" namespace="app" name="web" uid="sa-2" active />);
    await act(async () => { first.resolve(saDTO("sa-1", { declared: { namespace: "app", name: "web", uid: "sa-1", roleArn: { shown: "arn:aws:iam::123456789012:role/OLD" } } })); });
    expect(screen.getByTestId("identity-sa").textContent).toContain("Reading identity");
    expect(screen.getByTestId("identity-sa").textContent).not.toContain("OLD");
    await act(async () => { second.resolve(saDTO("sa-2")); });
    expect(screen.getByTestId("identity-declared").textContent).toContain("role/web");
    // a refresh reads again and holds nothing from the previous reply meanwhile
    const third = deferred<SAIdentityDTO>();
    saReply = async () => third.promise;
    fireEvent.click(screen.getByRole("button", { name: "Read again" }));
    expect(screen.getByTestId("identity-sa").textContent).toContain("Reading identity");
    await act(async () => { third.resolve(saDTO("sa-2")); });
    expect(screen.getByTestId("identity-declared")).toBeTruthy();
  });

  it("a pod whose ServiceAccount is gone compares nothing; a pod link opens UID-pinned", async () => {
    podReply = async (c, ns, n, u) => ({ cluster: c, namespace: ns, name: n, uid: u,
      report: { readAt: "2026-10-18T09:00:00Z", state: "read", saState: "not-found", report: report(pod("a"), null), limits: [LIMIT] } });
    render(<PodIdentity cluster="c" namespace="app" name="a" uid="u-a" active />);
    await waitFor(() => expect(screen.getByTestId("identity-pod").textContent).toContain("nothing is compared"));
    expect(screen.getByTestId("identity-uncompared").textContent).toContain("its ServiceAccount was not read");
    expect(screen.getByTestId("identity-pod").textContent).not.toContain("No identity configured");

    saReply = async (_c, _ns, _n, u) => saDTO(u, { pods: [report(pod("a"), [])] });
    render(<ServiceAccountIdentity cluster="c" namespace="app" name="web" uid="sa-1" active />);
    fireEvent.click(await screen.findByRole("button", { name: "a" }));
    expect(useFleet.getState().navIntent).toEqual({ kind: "select-pod", cluster: "c", namespace: "app", name: "a", uid: "u-a" });
  });

  it("a store says its base, the requested principal and a refusal; an unresolved controller names no subject; several controllers each say their own", async () => {
    storeReply = async (c, k, ns, n, u) => ({ cluster: c, kind: k, namespace: ns, name: n, uid: u, report: { readAt: "2026-10-18T09:00:00Z", state: "read",
      subject: { provider: "aws", mode: "jwt", base: "the ServiceAccount's token", serviceAccount: { name: "eso", referent: true },
        requested: ["arn:aws:iam::123456789012:role/extra", "arn:aws:iam::123456789012:role/main"], refusals: ["The role is not a valid ARN: ESO refuses it."] },
      handlers: { unresolved: "the controller's ReplicaSets were not read" }, limits: ["Cloud trust and permissions are not visible to Klyx."] } });
    const { unmount } = render(<StoreAuthenticatesAs cluster="c" kind="ClusterSecretStore" namespace="" name="aws" uid="s1" active />);
    const b = await screen.findByTestId("identity-handler-unresolved");
    expect(b.textContent).toContain("The subject is not named");
    const panel = screen.getByTestId("identity-store-auth");
    expect(panel.textContent).toContain("eso in each ExternalSecret's own namespace");
    expect(panel.textContent).toContain("role/extra → arn:aws:iam::123456789012:role/main");
    expect(within(screen.getByTestId("identity-refusal")).getByRole("img", { name: "look at" })).toBeTruthy();
    unmount();

    storeReply = async (c, k, ns, n, u) => ({ cluster: c, kind: k, namespace: ns, name: n, uid: u, report: { readAt: "2026-10-18T09:00:00Z", state: "read",
      subject: { provider: "aws", mode: "irsa", base: "the handling controller's own identity", controller: true },
      handlers: { groups: [
        { namespace: "external-secrets", serviceAccount: "sa-a", candidate: "external-secrets", pods: [pod("old", { containers: [{ name: "external-secrets", mechanisms: [{ kind: "irsa", state: "complete", role: { shown: "arn:aws:iam::123456789012:role/a" } }] }] })] },
        { namespace: "external-secrets", serviceAccount: "sa-b", candidate: "external-secrets", pods: [pod("new")] },
      ] }, limits: [] } });
    render(<StoreAuthenticatesAs cluster="c" kind="SecretStore" namespace="app" name="aws" uid="s2" active />);
    const hs = await screen.findAllByTestId("identity-handler");
    expect(hs.length).toBe(2);
    expect(hs[0].textContent).toContain("external-secrets: irsa arn:aws:iam::123456789012:role/a");
    expect(hs[1].textContent).toContain("no identity in the pod spec");
    expect(screen.getByTestId("identity-store-auth").textContent).toContain("each authenticates as its own identity");
  });

  it("a replaced or missing object is said, never shown in its place", async () => {
    saReply = async (_c, _ns, _n, u) => saDTO(u, { state: "replaced", declared: undefined });
    render(<ServiceAccountIdentity cluster="c" namespace="app" name="web" uid="sa-1" active />);
    await waitFor(() => expect(screen.getByTestId("identity-sa").textContent).toContain("replaced since it was opened"));
    expect(screen.queryByTestId("identity-declared")).toBeNull();
  });

  // review 1 (#8, #9): unread or uncompared is never "none"; omissions are
  // said whatever the list's length; every retained declaration is shown
  it("an uncompared pod says so and shows what its spec carries, never 'no identity configured'", async () => {
    const irsa = pod("a", { containers: [{ name: "app", mechanisms: [{ kind: "irsa", state: "complete", role: { shown: "arn:aws:iam::123456789012:role/web" } }] }] });
    podReply = async (c, ns, n, u) => ({ cluster: c, namespace: ns, name: n, uid: u,
      report: { readAt: "2026-10-18T09:00:00Z", state: "read", saState: "unreadable", report: report(irsa, null), limits: [] } });
    render(<PodIdentity cluster="c" namespace="app" name="a" uid="u-a" active />);
    const u = await screen.findByTestId("identity-uncompared");
    expect(u.textContent).toContain("irsa arn:aws:iam::123456789012:role/web");
    expect(screen.getByTestId("identity-pod").textContent).not.toContain("No identity configured");
  });

  it("pods all dropped by the reply's bound, or a continued page, never read as 'no pods'", async () => {
    saReply = async (_c, _ns, _n, u) => saDTO(u, { pods: [], podsRead: "more" }, { podsDropped: 3 });
    render(<ServiceAccountIdentity cluster="c" namespace="app" name="web" uid="sa-1" active />);
    await screen.findByTestId("identity-pods-dropped");
    expect(screen.getByTestId("identity-pods-more")).toBeTruthy();
    expect(screen.getByTestId("identity-sa").textContent).not.toContain("No pods run");
  });

  it("the stores' read state is said: unread and a partial controller join", async () => {
    saReply = async (_c, _ns, _n, u) => saDTO(u, { storesRead: "unread", storesGap: "the External Secrets stores are not read live and completely" });
    const { unmount } = render(<ServiceAccountIdentity cluster="c" namespace="app" name="web" uid="sa-1" active />);
    expect((await screen.findByTestId("identity-stores-unread")).textContent).toContain("are not known");
    unmount();
    saReply = async (_c, _ns, _n, u) => saDTO(u, { storesRead: "partial", storesGap: "Not known for 1 store(s) without auth, whose handling controller is not resolved: SecretStore app/x (no running controller pod)" });
    render(<ServiceAccountIdentity cluster="c" namespace="app" name="web" uid="sa-1" active />);
    expect((await screen.findByTestId("identity-stores-partial")).textContent).toContain("SecretStore app/x");
  });

  it("every retained declaration is shown, with its validation", async () => {
    saReply = async (_c, _ns, _n, u) => saDTO(u, { declared: { namespace: "app", name: "web", uid: u, stsRegional: { shown: "true" }, tokenExpiry: { shown: "86400" },
      azureExpiry: { shown: "90000" }, azureExpiryOutOfRange: true } });
    render(<ServiceAccountIdentity cluster="c" namespace="app" name="web" uid="sa-1" active />);
    const d = await screen.findByTestId("identity-declared");
    expect(d.textContent).toContain("AWS STS regional endpointstrue");
    expect(d.textContent).toContain("AWS token expiration86400s");
    expect(d.textContent).toContain("90000s: outside 3600-86400");
    expect(d.textContent).not.toContain("no identity annotations");
  });

  it("a store says the controller pods not yet running and those dropped by the bound", async () => {
    storeReply = async (c, k, ns, n, u) => ({ cluster: c, kind: k, namespace: ns, name: n, uid: u, podsDropped: 4, report: { readAt: "2026-10-18T09:00:00Z", state: "read",
      subject: { provider: "aws", mode: "default", base: "the handling controller's credential chain", controller: true },
      handlers: { groups: [{ namespace: "eso", serviceAccount: "sa", candidate: "eso/eso", pods: [] }], notRunning: ["eso-new (Pending)"] }, limits: [] } });
    render(<StoreAuthenticatesAs cluster="c" kind="SecretStore" namespace="app" name="s" uid="s1" active />);
    expect((await screen.findByTestId("identity-handler-not-running")).textContent).toContain("eso-new (Pending)");
    expect(screen.getByTestId("identity-handler-dropped").textContent).toContain("4 controller pods");
  });
});
