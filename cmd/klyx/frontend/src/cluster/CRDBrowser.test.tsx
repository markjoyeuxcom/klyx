import { describe, it, expect, beforeEach, vi } from "vitest";
import { render, fireEvent } from "@testing-library/react";
import { useFleet, CRDGroupDTO, crdCountKey } from "../store/fleet";
import { CRDBrowser } from "./CRDBrowser";

vi.mock("../bridge/crd", () => ({
  listCRDs: vi.fn(async () => {}),
  countKind: vi.fn(async () => {}),
}));
import { countKind } from "../bridge/crd";

const groups: CRDGroupDTO[] = [
  { group: "cilium.io", category: "CNI", kinds: [
    { kind: "CiliumEndpoint", plural: "ciliumendpoints", scope: "Namespaced", version: "v2", operator: "cilium", shortNames: ["cep"] },
    { kind: "CiliumNode", plural: "ciliumnodes", scope: "Cluster", version: "v2", operator: "cilium", shortNames: [] },
  ] },
  { group: "cert-manager.io", category: "PKI", kinds: [
    { kind: "Certificate", plural: "certificates", scope: "Namespaced", version: "v1", operator: "cert-manager", shortNames: ["cert"] },
  ] },
];

beforeEach(() => useFleet.setState({
  crd: { cluster: "x", groups, loading: false, expanded: [], counts: {}, groupBy: "group", search: "", builtinCategory: null },
}));

describe("CRDBrowser - CRD groups", () => {
	  it("renders groups with category badges", () => {
	    const { getByText } = render(<CRDBrowser cluster="x" />);
	    expect(getByText("cilium.io")).toBeTruthy();
	    expect(getByText("CNI")).toBeTruthy();
	    expect(getByText("cert-manager.io")).toBeTruthy();
	  });

	  it("owns scrolling for long CRD catalogs", () => {
	    const { getByTestId } = render(<CRDBrowser cluster="x" />);
	    expect(getByTestId("crd-resource-scroll").style.overflowY).toBe("auto");
	  });

  it("expands a group, shows kinds, and fires countKind", () => {
    const { getByText } = render(<CRDBrowser cluster="x" />);
    fireEvent.click(getByText("cilium.io"));
    expect(getByText("CiliumEndpoint")).toBeTruthy();
    expect(countKind).toHaveBeenCalledWith("x", "cilium.io", "v2", "ciliumendpoints");
  });

  it("renders 500+ when a count is capped", () => {
    useFleet.setState({ crd: { ...useFleet.getState().crd, expanded: ["cilium.io"], counts: { [crdCountKey("x", "cilium.io", "v2", "ciliumendpoints")]: { count: 500, capped: true } } } });
    const { getByText } = render(<CRDBrowser cluster="x" />);
    expect(getByText("500+")).toBeTruthy();
  });

  it("regroups by scope", () => {
    useFleet.setState({ crd: { ...useFleet.getState().crd, groupBy: "scope" } });
    const { getAllByText } = render(<CRDBrowser cluster="x" />);
    expect(getAllByText("Cluster").length).toBeGreaterThan(0);
    expect(getAllByText("Namespaced").length).toBeGreaterThan(0);
  });

  it("shows the empty state when there are no CRDs", () => {
    useFleet.setState({ crd: { ...useFleet.getState().crd, groups: [] } });
    const { getByText } = render(<CRDBrowser cluster="x" />);
    expect(getByText(/No custom resources/i)).toBeTruthy();
  });

  it("shows the empty state when search matches no CRD group", () => {
    useFleet.setState({ crd: { ...useFleet.getState().crd, search: "zzznomatch" } });
    const { getByText } = render(<CRDBrowser cluster="x" />);
    expect(getByText(/No custom resources/i)).toBeTruthy();
  });

  it("clicking a kind opens the resource drill-in", () => {
    useFleet.setState({ route: { name: "cluster", cluster: "x", section: "crds" }, crd: { ...useFleet.getState().crd, expanded: ["cilium.io"] } });
    const { getByText } = render(<CRDBrowser cluster="x" />);
    fireEvent.click(getByText("CiliumEndpoint"));
    const r = useFleet.getState().route;
    expect(r.name === "cluster" && r.resource?.kind).toBe("CiliumEndpoint");
    expect(r.name === "cluster" && r.resource?.plural).toBe("ciliumendpoints");
  });

  it("drill from crds section stays in crds section", () => {
    useFleet.setState({ route: { name: "cluster", cluster: "x", section: "crds" }, crd: { ...useFleet.getState().crd, expanded: ["cilium.io"] } });
    const { getByText } = render(<CRDBrowser cluster="x" />);
    fireEvent.click(getByText("CiliumEndpoint"));
    const r = useFleet.getState().route;
    if (r.name === "cluster") {
      expect(r.section).toBe("crds");
    }
  });

  it("search filters CRD groups - cert-manager visible but cilium hidden", () => {
    useFleet.setState({ crd: { ...useFleet.getState().crd, search: "certificate" } });
    const { queryByText } = render(<CRDBrowser cluster="x" />);
    expect(queryByText("cilium.io")).toBeNull();
    expect(queryByText("cert-manager.io")).toBeTruthy();
  });

  it("does not render the Built-in section header", () => {
    const { queryByText } = render(<CRDBrowser cluster="x" />);
    expect(queryByText("Built-in")).toBeNull();
  });
});
