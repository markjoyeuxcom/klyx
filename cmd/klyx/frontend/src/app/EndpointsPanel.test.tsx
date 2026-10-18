import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import type { EndpointRow, EndpointsState } from "../bridge/endpoints";

const getEndpoints = vi.fn();
const setCredential = vi.fn();
const removeCredential = vi.fn();
const moveToKeychain = vi.fn();
const testEndpoint = vi.fn();
vi.mock("../bridge/endpoints", () => ({
  getEndpoints: (...a: unknown[]) => getEndpoints(...a),
  setCredential: (...a: unknown[]) => setCredential(...a),
  removeCredential: (...a: unknown[]) => removeCredential(...a),
  moveToKeychain: (...a: unknown[]) => moveToKeychain(...a),
  testEndpoint: (...a: unknown[]) => testEndpoint(...a),
}));
import { EndpointsPanel, stateText } from "./EndpointsPanel";

const base = (over: Partial<EndpointRow>): EndpointRow => ({
  cluster: "prd", kind: "metrics", source: "https://mimir.example/prometheus", state: "missing", why: "no credential is stored for it - set one in Settings > External endpoints",
  legacy: false, storedToo: false, revision: "rev-1", canSet: true, canRemove: false, canMove: false, canTest: true, authType: "bearer", ...over,
});
const fleet = (rows: EndpointRow[]): EndpointsState => ({ rows });

beforeEach(() => vi.resetAllMocks());

// Coverage C5 §8: each endpoint with its state; a secret goes in with the
// revision the person saw, the field clears, and nothing reads it back.
describe("Settings > Endpoints", () => {
  it("stores a credential for the endpoint as reviewed, and never shows it", async () => {
    getEndpoints.mockResolvedValueOnce(fleet([base({}), base({ kind: "loki", source: "https://loki.example", canTest: false, testNote: "not implemented until S8: a successful GET of / is not a reader's availability", authType: "basic", username: "klyx" })]))
      .mockResolvedValueOnce(fleet([base({ state: "stored", why: undefined, canRemove: true, revision: "rev-1" })]));
    setCredential.mockResolvedValue({ ok: true, message: "Stored in the keychain for https://mimir.example:443." });
    render(<EndpointsPanel />);
    const input = await screen.findByLabelText("prd metrics token");
    expect((input as HTMLInputElement).type).toBe("password");
    expect((input as HTMLInputElement).value).toBe("");
    expect(screen.getByText(/Test: not implemented until S8/)).toBeTruthy();
    expect(screen.getByText("basic as klyx")).toBeTruthy();
    fireEvent.change(input, { target: { value: "s3cret-tok" } });
    fireEvent.click(screen.getByRole("button", { name: "Store the prd Metrics credential" }));
    await waitFor(() => expect(setCredential).toHaveBeenCalledWith("prd", "metrics", "rev-1", "s3cret-tok"));
    await screen.findByText("Stored in the keychain for https://mimir.example:443.");
    expect(document.body.textContent).not.toContain("s3cret-tok");
    expect(screen.getByTestId("endpoint-state").textContent).toBe("stored in the keychain");
  });

  it("moves a legacy token, and says what Remove does while it wins", async () => {
    getEndpoints.mockResolvedValue(fleet([base({ cluster: "dev", state: "legacy", why: undefined, legacy: true, storedToo: true, canSet: false, canMove: true, canRemove: true, revision: "rev-9",
      removeLabel: "Remove the keychain copy - fleet.yaml still holds a token, and that is what Klyx sends" })]));
    moveToKeychain.mockResolvedValue({ ok: false, message: "The token is stored in the keychain for https://prom.dev.example:443; fleet.yaml changed meanwhile and still holds a token - review it and move again." });
    render(<EndpointsPanel />);
    expect(await screen.findByText("in fleet.yaml in plain text, and stored in the keychain - finish the move")).toBeTruthy();
    expect(screen.queryByLabelText("dev metrics token")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Move the dev metrics token to the keychain" }));
    await waitFor(() => expect(moveToKeychain).toHaveBeenCalledWith("dev", "rev-9"));
    expect((await screen.findByTestId("endpoints-message")).textContent).toContain("fleet.yaml changed meanwhile");
    removeCredential.mockResolvedValue({ ok: true, message: "Removed the keychain copy. fleet.yaml still holds a token, and that is what Klyx sends." });
    fireEvent.click(screen.getByRole("button", { name: "Remove the keychain copy - fleet.yaml still holds a token, and that is what Klyx sends (dev Metrics)" }));
    await waitFor(() => expect(removeCredential).toHaveBeenCalledWith("dev", "metrics", "rev-9"));
  });

  it("tests metrics with the reviewed revision, and offers a retry when the state cannot be read", async () => {
    getEndpoints.mockResolvedValueOnce(null).mockResolvedValue(fleet([base({ state: "stored", why: undefined })]));
    testEndpoint.mockResolvedValue({ ok: false, message: "Unavailable: the endpoint's credential changed; read it again" });
    render(<EndpointsPanel />);
    fireEvent.click(await screen.findByRole("button", { name: "Retry" }));
    fireEvent.click(await screen.findByRole("button", { name: "Test the prd Metrics endpoint" }));
    await waitFor(() => expect(testEndpoint).toHaveBeenCalledWith("prd", "metrics", "rev-1"));
  });
});

describe("stateText", () => {
  it("says each state in words, warning on the ones that stop requests", () => {
    expect(stateText(base({ state: "not sent" }))).toEqual({ text: "not sent: proxied through the Kubernetes API server", warn: false });
    expect(stateText(base({ state: "for another origin", why: "the stored credential is for https://a:443; the endpoint is now https://b:443 - set it again" })).warn).toBe(true);
    expect(stateText(base({ state: "legacy", storedToo: false })).text).toBe("in fleet.yaml in plain text");
  });
});

// Codex's C5 implementation review, finding 1 (its probe, adopted): a typed
// credential is never stored for an endpoint that changed under it.
describe("a draft belongs to the endpoint as reviewed", () => {
  it("drops the draft when the endpoint's revision changes", async () => {
    const row = (revision: string, source: string) => base({ revision, source });
    getEndpoints.mockResolvedValueOnce(fleet([row("A", "https://a.example")])).mockResolvedValue(fleet([row("B", "https://b.example")]));
    testEndpoint.mockResolvedValue({ ok: false, message: "fleet.yaml changed" });
    setCredential.mockResolvedValue({ ok: true, message: "stored" });
    render(<EndpointsPanel />);
    fireEvent.change(await screen.findByLabelText("prd metrics token"), { target: { value: "SECRET-FOR-A" } });
    fireEvent.click(screen.getByRole("button", { name: "Test the prd Metrics endpoint" }));
    await screen.findByText("https://b.example");
    expect((screen.getByLabelText("prd metrics token") as HTMLInputElement).value).toBe("");
    expect((screen.getByRole("button", { name: "Store the prd Metrics credential" }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(screen.getByRole("button", { name: "Store the prd Metrics credential" }));
    expect(setCredential).not.toHaveBeenCalled();
  });
});
