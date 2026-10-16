import { describe, it, expect } from "vitest";
import { parseDescribe } from "./parse";
import { buildModel, conditionFault, timeOf, type VNode, type VTable } from "./model";
import pod from "./fixtures/pod.txt?raw";
import node from "./fixtures/node.txt?raw";
import ks from "./fixtures/ks.txt?raw";
import route from "./fixtures/route.txt?raw";
import deploy from "./fixtures/deploy.txt?raw";

const model = (t: string) => buildModel(parseDescribe(t));
function tables(ns: VNode[]): VTable[] {
  const out: VTable[] = [];
  const walk = (n: VNode) => {
    if (n.k === "table") out.push(n);
    else if (n.k === "kv" || n.k === "item") n.children.forEach(walk);
  };
  ns.forEach(walk);
  return out;
}

describe("conditionFault: Describe's own polarity (review 1)", () => {
  it("marks a positive type False and a negative type True, and nothing else", () => {
    expect(conditionFault("Ready", "False")).toBe(true);
    expect(conditionFault("Ready", "True")).toBe(false);
    expect(conditionFault("Stalled", "True")).toBe(true);
    expect(conditionFault("Stalled", "False")).toBe(false);
    expect(conditionFault("MemoryPressure", "True")).toBe(true);
    expect(conditionFault("Reconciling", "True")).toBe(false);
    expect(conditionFault("Reconciling", "False")).toBe(false);
    expect(conditionFault("Issuing", "True")).toBe(false);
    expect(conditionFault("Ready", "Unknown")).toBe(false);
    expect(conditionFault("SomethingCustom", "False")).toBe(false);
  });
});

describe("timeOf", () => {
  it("reads RFC 3339 and kubectl's RFC 1123Z, and only a whole value", () => {
    expect(timeOf("2026-10-10T18:34:40Z")).toBe(Date.UTC(2026, 9, 10, 18, 34, 40));
    expect(timeOf("Sat, 10 Oct 2026 12:29:38 +0100")).toBe(Date.UTC(2026, 9, 10, 11, 29, 38));
    expect(timeOf("restartedAt: 2026-10-10T19:46:27Z")).toBeUndefined();
    expect(timeOf("5d23h")).toBeUndefined();
  });
});

describe("buildModel", () => {
  it("numbers every run in the order it draws, and gives each section its range", () => {
    const m = model(pod);
    expect(m.runs.map((r) => r.id)).toEqual(m.runs.map((_, i) => i));
    let last = 0;
    for (const s of m.sections) {
      expect(s.from).toBe(last);
      expect(s.to).toBeGreaterThanOrEqual(s.from);
      last = s.to;
    }
    expect(last).toBe(m.runs.length);
  });

  it("sections a Pod in kubectl's order, with the loose fields unlabelled", () => {
    const m = model(pod);
    expect(m.sections.map((s) => s.label)).toEqual(["Overview", "Containers", "Conditions", "Volumes", null, "Events"]);
    const events = m.sections[m.sections.length - 1];
    expect(events.count).toBe(2);
    expect(events.aspect).toBe("attention");
    expect(events.aspectLabel).toBe("1 warning");
    const cond = m.sections.find((s) => s.label === "Conditions")!;
    expect(cond.aspect).toBe("fault");
    expect(cond.aspectLabel).toBe("2 conditions at fault");
    expect(m.uid).toBeNull();
  });

  it("marks a Warning event, and a Node's pressure conditions by their own polarity", () => {
    const ev = tables(model(pod).sections.find((s) => s.label === "Events")!.nodes)[0];
    expect(ev.kind).toBe("events");
    expect(ev.rows.map((r) => r.aspect ?? null)).toEqual([null, "attention"]);
    const nc = tables(model(node).sections.find((s) => s.label === "Conditions")!.nodes)[0];
    expect(nc.rows.map((r) => r.aspect ?? null)).toEqual([null, null, null, null]);
  });

  it("draws a known conditions list as a table with Message last, and a Kustomization's Ready False as a fault", () => {
    const m = model(ks);
    const t = tables(m.sections.find((s) => s.label === "Status")!.nodes)[0];
    expect(t.head.map((h) => h.text)).toEqual(["Type", "Status", "Reason", "Last Transition Time", "Observed Generation", "Message"]);
    expect(t.rows[0].cells.map((c) => c.text)).toEqual(["Ready", "False", "BuildFailed", "2026-10-10T15:30:38Z", "1", "kustomize build failed: accumulating resources: missing file ./legacy/old-ingress.yaml"]);
    expect(t.rows[0].aspect).toBe("fault");
    expect(m.uid).toBe("2493245b-cb33-45d2-a29e-902fca452722");
  });

  it("keeps an unknown kind's conditions as fields, never a table (review 3)", () => {
    const widget = ks.replace("kustomize.toolkit.fluxcd.io/v1", "example.com/v1").replace("Kind:         Kustomization", "Kind:         Widget");
    expect(tables(model(widget).sections.find((s) => s.label === "Status")!.nodes)).toHaveLength(0);
  });

  it("splits a route's two parent conditions into one table of two rows", () => {
    const t = tables(model(route).sections.find((s) => s.label === "Status")!.nodes);
    expect(t).toHaveLength(1);
    expect(t[0].rows.map((r) => r.cells[0].text)).toEqual(["Accepted", "ResolvedRefs"]);
    expect(t[0].rows[1].aspect).toBe("fault");
  });

  it("keeps a Deployment's Conditions table and its pod template inline", () => {
    const m = model(deploy);
    expect(m.sections.map((s) => s.label)).toEqual(["Overview", "Pod Template", "Conditions", null, "Events"]);
    expect(tables(m.sections.find((s) => s.label === "Conditions")!.nodes)[0].rows.every((r) => r.aspect === "fault")).toBe(true);
  });

  it("mutes kubectl's placeholders and times its timestamps", () => {
    const m = model(pod);
    expect(m.runs.find((r) => r.text === "<none>")?.muted).toBe(true);
    expect(m.runs.find((r) => r.text === "Sat, 10 Oct 2026 12:29:38 +0100")?.timeMs).toBe(Date.UTC(2026, 9, 10, 11, 29, 38));
  });

  it("keeps ConfigMap data as an opaque section of its own", () => {
    const cm = "Name:  cm\nNamespace:  default\n\nData\n====\nk:\n----\nv\n\nBinaryData\n====\n\nEvents:  <none>\n";
    const m = model(cm);
    expect(m.sections.map((s) => s.label)).toEqual(["Overview", "Data", "Events"]);
    expect(m.sections[1].nodes[0].k).toBe("opaque");
  });
});
