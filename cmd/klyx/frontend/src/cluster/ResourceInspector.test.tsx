import { describe, it, expect } from "vitest";
import { render } from "@testing-library/react";
import { InspectorConditions, InspectorEvents, InspectorIdle, InspectorStatus, conditionColor } from "./ResourceInspector";
import type { ConditionDTO, EventDTO } from "../store/fleet";

describe("InspectorStatus", () => {
  it("renders the verdict box and joins reason and detail into the sub-line", () => {
    const { getByText } = render(
      <InspectorStatus tone="danger" label="CrashLoopBackOff" reason="BackOff" detail="0/1 ready" />,
    );
    expect(getByText("CrashLoopBackOff")).toBeTruthy();
    expect(getByText("BackOff · 0/1 ready")).toBeTruthy();
  });
});

describe("InspectorIdle", () => {
  it("shows the attention count and keyboard hints when faults exist", () => {
    const { getByText } = render(<InspectorIdle subject="pods" attention={3} hint="Select a pod." />);
    expect(getByText("3 pods need attention")).toBeTruthy();
    expect(getByText("j k nav · ↵ inspect")).toBeTruthy();
  });

  it("reads quiet when nothing needs attention", () => {
    const { getByText } = render(<InspectorIdle subject="releases" attention={0} hint="Select a release." />);
    expect(getByText("all releases quiet")).toBeTruthy();
  });
});

describe("conditionColor", () => {
  it("honors negative-polarity conditions", () => {
    // Ready-style: True is healthy.
    expect(conditionColor("True", "Ready")).toContain("success");
    expect(conditionColor("False", "Ready")).toContain("danger");
    // Pressure-style: True is trouble, False is healthy.
    expect(conditionColor("True", "MemoryPressure")).toContain("danger");
    expect(conditionColor("False", "MemoryPressure")).toContain("success");
    // HPA ScalingLimited=False means "within range" - never a red dot.
    expect(conditionColor("False", "ScalingLimited")).toContain("success");
    expect(conditionColor("Unknown", "Ready")).toContain("info");
    // the registry (slice 15 review 1): Stalled is negative, and a type it
    // does not know is neutral, never assumed positive
    expect(conditionColor("True", "Stalled")).toContain("danger");
    expect(conditionColor("False", "Stalled")).toContain("success");
    expect(conditionColor("False", "Frobbed")).toContain("info");
  });
});

describe("InspectorConditions", () => {
  const conditions: ConditionDTO[] = [
    { type: "Ready", status: "True", reason: "MinimumReplicasAvailable", message: "Deployment has minimum availability." },
    { type: "Progressing", status: "False", reason: "ProgressDeadlineExceeded", message: "exceeded its progress deadline" },
  ];

  it("renders type, reason chip, and message for each condition", () => {
    const { getByText } = render(<InspectorConditions conditions={conditions} />);
    expect(getByText("Ready")).toBeTruthy();
    expect(getByText("MinimumReplicasAvailable")).toBeTruthy();
    expect(getByText("exceeded its progress deadline")).toBeTruthy();
  });

  it("accepts a severity override for kind-specific rules", () => {
    const { container } = render(
      <InspectorConditions
        conditions={[{ type: "MemoryPressure", status: "Unknown", reason: "", message: "" }]}
        colorOf={() => "var(--color-text-danger)"}
      />,
    );
    // the override keeps its rule; the mark is its presentation
    expect(container.querySelector(".cond")?.getAttribute("data-aspect")).toBe("fault");
  });

  it("marks by the shared polarity registry: Stalled is negative, an unknown type is neutral (slice 15 review 1)", () => {
    const { container } = render(
      <InspectorConditions conditions={[
        { type: "Stalled", status: "True", reason: "", message: "" },
        { type: "Stalled", status: "False", reason: "", message: "" },
        { type: "Frobbed", status: "False", reason: "", message: "" },
        { type: "Ready", status: "Unknown", reason: "", message: "" },
        { type: "Ready", status: "True", reason: "", message: "" },
      ]} />,
    );
    expect([...container.querySelectorAll(".cond")].map((c) => c.getAttribute("data-aspect"))).toEqual(["fault", "none", "none", "checking", "none"]);
  });

  it("says when a condition was recorded for an older generation, and how old it is", () => {
    const at = new Date(Date.now() - 3.5 * 86400_000).toISOString();
    const { getByText } = render(<InspectorConditions generation={4} conditions={[{ type: "Ready", status: "False", reason: "Broken", message: "", lastTransition: at, observedGeneration: 3 }]} />);
    expect(getByText("for generation 3 of 4")).toBeTruthy();
    expect(getByText("3d ago")).toBeTruthy();
  });

  it("says so when there are no conditions", () => {
    const { getByText } = render(<InspectorConditions conditions={[]} />);
    expect(getByText("No conditions reported.")).toBeTruthy();
  });
});

describe("InspectorEvents", () => {
  // Offsets sit mid-bucket so the Age ticker's 30s quantisation cannot move
  // them across a unit boundary: 150s floors to 2m, 3660s floors to 1h.
  const events: EventDTO[] = [
    { type: "Warning", reason: "BackOff", message: "back-off restarting failed container", count: 28000, lastSeen: new Date(Date.now() - 150_000).toISOString() },
    { type: "Normal", reason: "Pulled", message: "image pulled", count: 1, lastSeen: new Date(Date.now() - 3_660_000).toISOString() },
  ];

  it("renders reason, message, compact count with exact hover, and a ticking age", () => {
    const { getByText, getByTitle } = render(<InspectorEvents events={events} />);
    expect(getByText("BackOff")).toBeTruthy();
    expect(getByTitle("28,000 occurrences").textContent).toBe("×28k");
    expect(getByText("2m")).toBeTruthy();
    expect(getByText("1h")).toBeTruthy();
  });

  it("uses the caller's empty text", () => {
    const { getByText } = render(<InspectorEvents events={[]} emptyText="No events for this node." />);
    expect(getByText("No events for this node.")).toBeTruthy();
  });
});

describe("InspectorKV inside the template (live pass)", () => {
  it("keeps data keys in their real case and wraps values", async () => {
    const { InspectorTemplate } = await import("../layout/InspectorLayout");
    const { InspectorKV } = await import("./ResourceInspector");
    const long = "x".repeat(200);
    const { getByText } = render(
      <InspectorTemplate.Provider value={true}>
        <InspectorKV rows={[{ label: "a", value: long }]} />
      </InspectorTemplate.Provider>,
    );
    const key = getByText("a");
    expect(key.style.textTransform).toBe("");
    expect(getByText(long).style.overflowWrap).toBe("anywhere");
  });
});
