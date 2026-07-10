import { describe, it, expect, beforeEach } from "vitest";
import { render, fireEvent } from "@testing-library/react";
import { useFleet } from "../store/fleet";
import { ManifestButton } from "./ManifestButton";

function seedRoute() {
  useFleet.setState({ route: { name: "cluster", cluster: "x", section: "gitops" } });
}

describe("ManifestButton", () => {
  beforeEach(() => seedRoute());

  it("navigates to the single guarded editor with the resolved ref", () => {
    const { getByText } = render(<ManifestButton group="kustomize.toolkit.fluxcd.io" kind="Kustomization" namespace="flux-system" name="apps" />);
    fireEvent.click(getByText("manifest"));
    const s = useFleet.getState();
    if (s.route.name !== "cluster") throw new Error("route lost");
    expect(s.route.section).toBe("resources");
    expect(s.route.resource?.plural).toBe("kustomizations");
    expect(s.route.instance).toEqual({ namespace: "flux-system", name: "apps" });
  });

  it("drops the namespace for a cluster-scoped kind", () => {
    const { getByText } = render(<ManifestButton group="cert-manager.io" kind="ClusterIssuer" namespace="ignored" name="le-prod" />);
    fireEvent.click(getByText("manifest"));
    const s = useFleet.getState();
    if (s.route.name !== "cluster") throw new Error("route lost");
    expect(s.route.instance).toEqual({ namespace: "", name: "le-prod" });
  });

  it("renders nothing for an unknown kind (honest degrade, no dead button)", () => {
    const { queryByText } = render(<ManifestButton group="unknown.io" kind="Mystery" namespace="ns" name="x" />);
    expect(queryByText("manifest")).toBeNull();
  });
});
