import { describe, it, expect, beforeEach } from "vitest";
import { render } from "@testing-library/react";
import { useFleet } from "../store/fleet";
import { Breadcrumb } from "./Breadcrumb";

beforeEach(() => useFleet.setState({ clusters: [], route: { name: "fleet" } }));

describe("Breadcrumb", () => {
  it("shows Fleet at the root", () => {
    const { getByText } = render(<Breadcrumb />);
    expect(getByText("Fleet")).toBeTruthy();
  });
  it("shows Fleet / cluster / Section and Fleet navigates back", () => {
    useFleet.setState({ route: { name: "cluster", cluster: "homelab-nelli", section: "gitops" } });
    const { getByText } = render(<Breadcrumb />);
    expect(getByText("homelab-nelli")).toBeTruthy();
    // gitops section label is "Flux" (design principle 8)
    expect(getByText("Flux")).toBeTruthy();
    getByText("Fleet").click();
    expect(useFleet.getState().route).toEqual({ name: "fleet" });
  });
  it("the cluster-name segment returns to Overview", () => {
    useFleet.setState({ route: { name: "cluster", cluster: "homelab-nelli", section: "gitops" } });
    const { getByText } = render(<Breadcrumb />);
    getByText("homelab-nelli").click();
    expect(useFleet.getState().route).toMatchObject({ name: "cluster", cluster: "homelab-nelli", section: "overview" });
  });
  it("shows the kind crumb when a resource is selected", () => {
    useFleet.setState({ route: { name: "cluster", cluster: "x", section: "resources", resource: { group: "cilium.io", version: "v2", plural: "ciliumendpoints", kind: "CiliumEndpoint", scope: "Namespaced" } } });
    const { getByText } = render(<Breadcrumb />);
    expect(getByText("CiliumEndpoint")).toBeTruthy();
    expect(getByText("Resources")).toBeTruthy();
  });
  it("shows the instance name crumb when an instance is selected", () => {
    useFleet.setState({ route: { name: "cluster", cluster: "x", section: "resources", resource: { group: "cert-manager.io", version: "v1", plural: "certificates", kind: "Certificate", scope: "Namespaced" }, instance: { namespace: "default", name: "web-tls" } } });
    const { getByText } = render(<Breadcrumb />);
    expect(getByText("web-tls")).toBeTruthy();
    expect(getByText("Certificate")).toBeTruthy();
  });
  it("shows the gateway crumb when a gateway is selected", () => {
    useFleet.setState({ route: { name: "cluster", cluster: "x", section: "gatewayApi", gateway: { namespace: "infra", name: "eg" } } });
    const { getByText } = render(<Breadcrumb />);
    expect(getByText("eg")).toBeTruthy();
    expect(getByText("Gateway API")).toBeTruthy();
  });
});
