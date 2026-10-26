import { EndpointsService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";

// Settings > Endpoints (coverage C5 §8). A credential goes in through
// setCredential and never comes back: the state says only where it is.
// Every action sends the revision the person saw; the backend refuses it if
// fleet.yaml moved since.

export type EndpointRow = {
  cluster: string;
  kind: string;
  source: string;
  tenant?: string;
  // tlsOff: its certificate is not verified; scope its match in words;
  // unscoped: an Alertmanager or Loki with no match (Settings review slice 0)
  tlsOff?: boolean;
  scope?: string;
  unscoped?: boolean;
  authType?: string;
  username?: string;
  state: string;
  why?: string;
  legacy: boolean;
  storedToo: boolean;
  revision: string;
  canSet: boolean;
  canRemove: boolean;
  canMove: boolean;
  canTest: boolean;
  testNote?: string;
  removeLabel?: string;
};

export type EndpointsState = { rows: EndpointRow[]; error?: string };
export type EndpointAction = { ok: boolean; message: string };

function failed(e: unknown): EndpointAction {
  return { ok: false, message: e instanceof Error ? e.message : String(e) };
}

export async function getEndpoints(): Promise<EndpointsState | null> {
  try {
    return (await EndpointsService.GetEndpoints()) as EndpointsState;
  } catch (e) {
    console.error("endpoints", e);
    return null;
  }
}

export async function setCredential(cluster: string, kind: string, revision: string, secret: string): Promise<EndpointAction> {
  try {
    return (await EndpointsService.SetCredential(cluster, kind, revision, secret)) as EndpointAction;
  } catch (e) {
    return failed(e);
  }
}

export async function removeCredential(cluster: string, kind: string, revision: string): Promise<EndpointAction> {
  try {
    return (await EndpointsService.RemoveCredential(cluster, kind, revision)) as EndpointAction;
  } catch (e) {
    return failed(e);
  }
}

export async function moveToKeychain(cluster: string, revision: string): Promise<EndpointAction> {
  try {
    return (await EndpointsService.MoveToKeychain(cluster, revision)) as EndpointAction;
  } catch (e) {
    return failed(e);
  }
}

export async function testEndpoint(cluster: string, kind: string, revision: string): Promise<EndpointAction> {
  try {
    return (await EndpointsService.TestEndpoint(cluster, kind, revision)) as EndpointAction;
  } catch (e) {
    return failed(e);
  }
}
