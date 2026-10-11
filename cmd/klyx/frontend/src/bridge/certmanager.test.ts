import { describe, it, expect, vi, beforeEach } from "vitest";
import { useFleet } from "../store/fleet";
import type { CertChainDTO } from "../store/fleet";

type Deferred<T> = { promise: Promise<T>; resolve: (v: T) => void; reject: (e: unknown) => void };
function deferred<T>(): Deferred<T> {
  let resolve!: (v: T) => void; let reject!: (e: unknown) => void;
  const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej; });
  return { promise, resolve, reject };
}
const calls: Deferred<unknown>[] = [];
vi.mock("../../bindings/github.com/moomora/klyx/internal/appbridge/index.js", () => ({
  CertManagerService: {
    Open: vi.fn(async () => {}),
    Close: vi.fn(async () => {}),
    GetCertificateChain: vi.fn(() => { const d = deferred<unknown>(); calls.push(d); return d.promise; }),
  },
}));
const handlers: Record<string, (ev: unknown) => void> = {};
vi.mock("@wailsio/runtime", () => ({ Events: { On: vi.fn((n: string, h: (ev: unknown) => void) => { handlers[n] = h; return () => {}; }) } }));
import { getCertificateChain } from "./certmanager";

const chainFor = (marker: string): CertChainDTO => ({
  certificate: { rule: { included: false, role: "ok", label: "Ready" }, namespace: "ns", name: "c", ready: "Ready", reason: marker, message: "", issuerKind: "", issuerName: "", secretName: "", dnsNames: [], notAfterUnix: 0, expiresInSeconds: 0, renewalUnix: 0 },
  issuer: null, requests: [], failingLink: null, warnings: [],
});

beforeEach(() => {
  calls.length = 0;
  useFleet.setState({ certmanager: { cluster: "A", certificates: [], issuers: [], loading: false, watchError: null, selectedKey: "ns/c", chain: null, chainLoading: true, chainCluster: null } });
});

describe("cert-manager bridge fences (redesign step 4c)", () => {
  it("a late chain from A never lands under the same key on B", async () => {
    const p = getCertificateChain("A", "ns", "c");
    useFleet.getState().setCertManagerLoading("B");
    useFleet.getState().selectCertificate("ns/c");
    calls[0].resolve(chainFor("from-A"));
    await p;
    expect(useFleet.getState().certmanager.chain).toBeNull();
  });

  it("overlapping chain reads: only the newest lands, recorded with its cluster", async () => {
    const older = getCertificateChain("A", "ns", "c");
    const newer = getCertificateChain("A", "ns", "c");
    calls[1].resolve(chainFor("newest"));
    await newer;
    calls[0].resolve(chainFor("stale"));
    await older;
    expect(useFleet.getState().certmanager.chain?.certificate.reason).toBe("newest");
    expect(useFleet.getState().certmanager.chainCluster).toBe("A");
  });


});
