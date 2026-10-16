import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, fireEvent, act } from "@testing-library/react";

vi.mock("../../bridge/exec", () => ({ describeResource: vi.fn() }));
vi.mock("../../bridge/crd", () => ({ copyText: vi.fn().mockResolvedValue(undefined) }));
import { describeResource } from "../../bridge/exec";
import { describeArg, useDescribe, type DescribeTarget } from "./useDescribe";
import { InspectorLayout } from "../../layout/InspectorLayout";

describe("describeArg (slice 14c)", () => {
  it("names each lens kind by singular.group, and nothing it does not know", () => {
    expect(describeArg("Kustomization")).toBe("kustomization.kustomize.toolkit.fluxcd.io");
    expect(describeArg("HelmRelease")).toBe("helmrelease.helm.toolkit.fluxcd.io");
    expect(describeArg("GitRepository")).toBe("gitrepository.source.toolkit.fluxcd.io");
    expect(describeArg("Application")).toBe("application.argoproj.io");
    expect(describeArg("ClusterIssuer")).toBe("clusterissuer.cert-manager.io");
    expect(describeArg("ClusterSecretStore")).toBe("clustersecretstore.external-secrets.io");
    expect(describeArg("HTTPRoute")).toBe("httproute.gateway.networking.k8s.io");
    expect(describeArg("StatefulSet")).toBe("statefulset.apps");
    expect(describeArg("CronJob")).toBe("cronjob.batch");
    expect(describeArg("NodeClaim")).toBe("nodeclaim.karpenter.sh");
    expect(describeArg("Widget")).toBeNull();
    // a Helm release is not a Kubernetes object
    expect(describeArg("Release")).toBeNull();
  });
});

function Probe({ cluster, target }: { cluster: string; target: DescribeTarget | null }) {
  const d = useDescribe(cluster, target);
  return (
    <div>
      {d.menu.map((m) => ("label" in m && "onSelect" in m ? <button key={m.label} onClick={m.onSelect}>{m.label}</button> : null))}
      {d.sheet}
    </div>
  );
}

describe("useDescribe", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(describeResource).mockImplementation(() => Object.assign(Promise.resolve({ ok: true, output: "Name:  apps\n", error: "", command: "kubectl describe", commandError: "" }), { cancel: vi.fn() }) as never);
  });

  it("offers Describe and opens the sheet for its object", async () => {
    const t = { kind: "Kustomization", namespace: "flux-system", name: "apps", uid: "u1" };
    const { getByRole, findByTestId } = render(<Probe cluster="prd" target={t} />);
    fireEvent.click(getByRole("button", { name: "Describe" }));
    await findByTestId("describe-sheet");
    expect(describeResource).toHaveBeenCalledWith("prd", "kustomization.kustomize.toolkit.fluxcd.io", "flux-system", "apps");
  });

  it("returns focus to More when the sheet closes (review 1 probe)", async () => {
    function Inspector() {
      const d = useDescribe("a", { kind: "Kustomization", namespace: "shop", name: "old", uid: "u1" });
      return <><InspectorLayout pin={{ cluster: "a", uid: "u1" }} kind="Kustomization" name="old" onClose={() => {}} more={d.menu}>evidence</InspectorLayout>{d.sheet}</>;
    }
    const r = render(<Inspector />);
    const more = r.getByRole("button", { name: "More" });
    fireEvent.click(more);
    const item = r.getByRole("menuitem", { name: "Describe" });
    item.focus();
    fireEvent.click(item);
    await r.findByTestId("describe-formatted");
    fireEvent.click(r.getByRole("button", { name: "Close describe" }));
    expect(document.activeElement).toBe(more);
  });

  it("offers nothing for no target or an unknown kind", () => {
    const none = render(<Probe cluster="prd" target={null} />);
    expect(none.queryByRole("button", { name: "Describe" })).toBeNull();
    none.unmount();
    const unknown = render(<Probe cluster="prd" target={{ kind: "Widget", namespace: "a", name: "b" }} />);
    expect(unknown.queryByRole("button", { name: "Describe" })).toBeNull();
  });

  it("never hands an open sheet to another object, and closes for a removed one", async () => {
    const a = { kind: "Kustomization", namespace: "flux-system", name: "apps", uid: "u1" };
    const { getByRole, rerender, queryByTestId, findByTestId } = render(<Probe cluster="prd" target={a} />);
    fireEvent.click(getByRole("button", { name: "Describe" }));
    await findByTestId("describe-sheet");
    rerender(<Probe cluster="prd" target={{ ...a, name: "infra", uid: "u2" }} />);
    expect(queryByTestId("describe-sheet")).toBeNull();
    expect(describeResource).toHaveBeenCalledTimes(1);
    rerender(<Probe cluster="prd" target={a} />);
    // reopening is the person's choice, never automatic
    expect(queryByTestId("describe-sheet")).toBeNull();
    fireEvent.click(getByRole("button", { name: "Describe" }));
    await findByTestId("describe-sheet");
    rerender(<Probe cluster="prd" target={null} />);
    await act(async () => {});
    expect(queryByTestId("describe-sheet")).toBeNull();
  });
});
