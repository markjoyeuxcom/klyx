import { describe, it, expect, vi } from "vitest";
import { render, fireEvent } from "@testing-library/react";
import type { ConditionDTO, CRDDefinitionDTO } from "../store/fleet";

vi.mock("../bridge/crd", () => ({ getCRDDefinition: vi.fn(), copyText: vi.fn(async () => {}) }));
vi.mock("../bridge/exec", () => ({ describeResource: vi.fn() }));
import { getCRDDefinition } from "../bridge/crd";
import { CRDDefinitionInspector, conversionWords, definitionFindings, SchemaTree } from "./CRDDefinition";

const c = (type: string, status: string, extra: Partial<ConditionDTO> = {}): ConditionDTO => ({ type, status, reason: "", message: "", ...extra });

describe("definitionFindings (slice 15c, design reviews 1 and 2)", () => {
  it("an ordinary healthy CRD has no finding: Established, NamesAccepted, no NonStructuralSchema", () => {
    expect(definitionFindings([c("Established", "True"), c("NamesAccepted", "True")])).toEqual([]);
    expect(definitionFindings([c("Established", "True"), c("NamesAccepted", "True"), c("NonStructuralSchema", "False")])).toEqual([]);
  });
  it("names accepted before establishment is not yet established, never 'not served'", () => {
    expect(definitionFindings([c("Established", "False", { reason: "Installing" }), c("NamesAccepted", "True")])).toEqual([{ aspect: "attention", text: "Not yet established (names accepted): Installing" }]);
  });
  it("a later naming conflict sits beside Established True", () => {
    expect(definitionFindings([c("Established", "True"), c("NamesAccepted", "False", { message: "\"wd\" is already in use" })])).toEqual([{ aspect: "attention", text: "Names not accepted: \"wd\" is already in use" }]);
  });
  it("a non-structural schema is a look-at; an explicit Unknown is checking", () => {
    expect(definitionFindings([c("Established", "True"), c("NamesAccepted", "True"), c("NonStructuralSchema", "True", { message: "spec.x: type missing" })]))
      .toEqual([{ aspect: "attention", text: "Non-structural schema: spec.x: type missing" }]);
    expect(definitionFindings([c("Established", "True"), c("NamesAccepted", "True"), c("NonStructuralSchema", "Unknown")])).toEqual([{ aspect: "checking", text: "NonStructuralSchema is Unknown" }]);
  });
  it("a missing expected condition is checking", () => {
    expect(definitionFindings([c("Established", "True")])).toEqual([{ aspect: "checking", text: "NamesAccepted is not recorded" }]);
  });
});

describe("conversionWords", () => {
  it("None says the schemas may differ; a webhook names its URL or service", () => {
    expect(conversionWords({ strategy: "None" })).toBe("None: the API server changes only apiVersion; other fields pass through unchanged, even where the versions' schemas differ.");
    expect(conversionWords({ strategy: "Webhook", url: "https://conv.example/convert", reviewVersions: ["v1"] })).toBe("Webhook: https://conv.example/convert, review versions v1.");
    expect(conversionWords({ strategy: "Webhook", serviceNamespace: "ops", serviceName: "conv", servicePort: 443 })).toBe("Webhook: ops/conv:443.");
  });
});

const def: CRDDefinitionDTO = {
  name: "widgets.example.com", group: "example.com", kind: "Widget", plural: "widgets", singular: "widget", listKind: "WidgetList", scope: "Namespaced",
  shortNames: ["wd"], categories: [], operator: "", uid: "u", created: "",
  versions: [
    { name: "v1beta1", served: false, storage: false, deprecated: true, deprecationWarning: "use v1", columns: [] },
    { name: "v1", served: true, storage: true, deprecated: false, columns: [{ name: "Ready", type: "string", jsonPath: ".status.x", priority: 0 }] },
  ],
  storedVersions: ["v1beta1", "v1"], conversion: { strategy: "None" }, conditions: [c("Established", "True"), c("NamesAccepted", "True")],
  schemaVersion: "v1", schemaRecorded: true,
  schema: [
    { path: [], type: "object", description: "A widget." },
    { path: [{ k: "p", n: "spec" }], type: "object" },
    { path: [{ k: "p", n: "spec" }, { k: "p", n: "mode" }], type: "string", required: true, description: "How fast." },
    { path: [{ k: "p", n: "spec" }, { k: "p", n: "deep" }], type: "object" },
    { path: [{ k: "p", n: "spec" }, { k: "p", n: "deep" }, { k: "p", n: "leaf" }], type: "integer" },
    { path: [{ k: "p", n: "status" }], type: "object" },
  ],
};

describe("CRDDefinitionInspector", () => {
  it("draws the definition: versions, stored-not-served, conversion, columns, schema", async () => {
    vi.mocked(getCRDDefinition).mockResolvedValue(def);
    const { findByTestId, getByText, getByTestId } = render(<CRDDefinitionInspector cluster="x" sel={{ group: "example.com", plural: "widgets", kind: "Widget", version: "v1", scope: "Namespaced" }} count="3" onClose={() => {}} onOpen={() => {}} />);
    await findByTestId("crd-facts");
    expect(getByText("Established")).toBeTruthy();
    expect(getByTestId("crd-facts").textContent).toContain("Instances3");
    expect(getByText("Objects may still be stored as v1beta1, which is no longer served.")).toBeTruthy();
    expect(getByTestId("crd-columns").textContent).toContain(".status.x");
    expect(getByTestId("crd-conversion").textContent).toContain("pass through unchanged");
  });
});

describe("SchemaTree", () => {
  it("opens the top level, hides deeper levels until opened, and filters by path with ancestors", () => {
    const { getByText, queryByText, getByLabelText, getByPlaceholderText } = render(<SchemaTree nodes={def.schema} recorded truncated={false} />);
    expect(getByText("mode")).toBeTruthy();
    expect(queryByText("leaf")).toBeNull();
    fireEvent.click(getByLabelText("Expand deep"));
    expect(getByText("leaf")).toBeTruthy();
    fireEvent.change(getByPlaceholderText("Filter fields (spec.mode)"), { target: { value: "deep.leaf" } });
    expect(getByText("leaf")).toBeTruthy();
    expect(getByText("deep")).toBeTruthy();
    expect(queryByText("mode")).toBeNull();
  });

  it("says when the schema was cut short", () => {
    const { getByText } = render(<SchemaTree nodes={def.schema} recorded truncated />);
    expect(getByText(/more fields than this view keeps/)).toBeTruthy();
  });

  // implementation review 1: the root's own facts are drawn, and a recorded
  // schema with no properties is not "no schema".
  it("draws the root's facts above the fields", () => {
    const { getByTestId } = render(<SchemaTree nodes={def.schema} recorded truncated={false} />);
    expect(getByTestId("crd-schema-root").textContent).toBe("RootobjectA widget.");
  });

  it("a free-form schema declares no fields, and keeps unknown ones", () => {
    const root = { path: [], type: "object", preserveUnknown: true, constraints: ["maxProperties"], description: "Free-form payload" };
    const { getByTestId, queryByText, queryByPlaceholderText } = render(<SchemaTree nodes={[root]} recorded truncated={false} />);
    expect(getByTestId("crd-schema-root").textContent).toBe("Rootobject · keeps unknown fields · constraints not shown: maxPropertiesFree-form payload");
    expect(getByTestId("crd-schema-nofields").textContent).toBe("No fields declared. Unknown fields are kept, so the object's fields are not pruned.");
    expect(queryByText("This version records no schema.")).toBeNull();
    expect(queryByPlaceholderText("Filter fields (spec.mode)")).toBeNull();
  });

  it("a version with no schema says so", () => {
    const { getByText, queryByTestId } = render(<SchemaTree nodes={[]} recorded={false} truncated={false} />);
    expect(getByText("This version records no schema.")).toBeTruthy();
    expect(queryByTestId("crd-schema-nofields")).toBeNull();
  });
});
