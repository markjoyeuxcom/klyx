import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render } from "@testing-library/react";
import { FieldsBlocks, OWN_FIELD_BLOCKS } from "./genericInspector";
import type { InstanceDetailDTO } from "../store/fleet";

afterEach(cleanup);

const detail = (fieldsJSON: string) => ({ fieldsJSON }) as InstanceDetailDTO;

describe("FieldsBlocks (implementation review 1)", () => {
  it("draws an int64 exactly as the server wrote it", () => {
    const { container } = render(<FieldsBlocks d={detail('{"spec":{"offset":9007199254740993},"weight":0.10}')} />);
    expect(container.textContent).toContain("9007199254740993");
    expect(container.textContent).toContain("0.10");
  });

  it("draws spec and status first, then the other fields, each in a block", () => {
    const { container } = render(<FieldsBlocks d={detail('{"data":{"a":"1"},"status":{"phase":"Ready"},"spec":{"mode":"fast"}}')} />);
    expect(container.querySelectorAll(".insp-block").length).toBe(3);
    const text = container.textContent!;
    expect(text.indexOf("mode")).toBeLessThan(text.indexOf("phase"));
    expect(text.indexOf("phase")).toBeLessThan(text.indexOf("Data"));
  });

  it("bounds the blocks however many top-level fields an object has, and says so", () => {
    const many = Object.fromEntries(Array.from({ length: 5000 }, (_, i) => [`field${i}`, i]));
    const { container, getByTestId } = render(<FieldsBlocks d={detail(JSON.stringify({ spec: { a: 1 }, ...many }))} />);
    // spec, OWN_FIELD_BLOCKS others, and one shared tree
    expect(container.querySelectorAll(".insp-block").length).toBe(1 + OWN_FIELD_BLOCKS + 1);
    expect(getByTestId("fields-rest").textContent).toContain((5000 - OWN_FIELD_BLOCKS).toLocaleString());
    expect(container.textContent).toMatch(/not shown/);
  });

  it("says when the fields cannot be drawn", () => {
    const { container } = render(<FieldsBlocks d={detail("{not json")} />);
    expect(container.textContent).toContain("could not be drawn");
  });
});

// Coverage S9 §7: an owner links only to the related ref of its own group,
// kind and name.
describe("ObjectBlock owner links (coverage S9)", () => {
  it("links a ScaledObject owner by its group; a same-named kind elsewhere stays text", async () => {
    const { ObjectBlock } = await import("./genericInspector");
    const { fireEvent, screen } = await import("@testing-library/react");
    const { useFleet } = await import("../store/fleet");
    const d = { kind: "HorizontalPodAutoscaler", namespace: "team", name: "web-autoscaler", created: "", labels: {}, conditions: [], events: [], yaml: "",
      owners: [{ apiVersion: "keda.sh/v1alpha1", kind: "ScaledObject", name: "web", controller: true },
        { apiVersion: "example.com/v1", kind: "ScaledObject", name: "web" }] } as unknown as InstanceDetailDTO;
    const related = [{ relation: "owner", kind: "ScaledObject", namespace: "team", name: "web", group: "keda.sh", version: "v1alpha1", plural: "scaledobjects", scope: "Namespaced" }];
    useFleet.getState().openCluster("x");
    render(<ObjectBlock cluster="x" d={d} related={related} />);
    const links = screen.getAllByRole("button").filter((b) => b.textContent?.includes("ScaledObject"));
    expect(links).toHaveLength(1);
    fireEvent.click(links[0]);
    expect(useFleet.getState().route).toMatchObject({ resource: { group: "keda.sh", plural: "scaledobjects" } });
  });
});
