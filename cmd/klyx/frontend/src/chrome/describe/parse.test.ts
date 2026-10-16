import { describe, it, expect } from "vitest";
import { parseDescribe, printDescribe, type DField, type DNode, type DTable, type DTree } from "./parse";
import gateway from "./fixtures/gateway.txt?raw";
import route from "./fixtures/route.txt?raw";
import ks from "./fixtures/ks.txt?raw";
import cert from "./fixtures/cert.txt?raw";
import es from "./fixtures/es.txt?raw";
import pod from "./fixtures/pod.txt?raw";
import node from "./fixtures/node.txt?raw";
import deploy from "./fixtures/deploy.txt?raw";
import svc from "./fixtures/svc.txt?raw";

const LAB = { gateway, route, ks, cert, es, pod, node, deploy, svc };

const fields = (nodes: DNode[]) => nodes.filter((n): n is DField => n.t === "field");
function at(tree: DTree | DNode[], ...path: string[]): DField {
  let nodes = Array.isArray(tree) ? tree : tree.nodes;
  let f: DField | undefined;
  for (const k of path) {
    f = fields(nodes).find((x) => x.key === k);
    if (!f) throw new Error(`no field ${k} in [${fields(nodes).map((x) => x.key).join(", ")}]`);
    nodes = f.children;
  }
  return f!;
}
const tableIn = (f: DField) => f.children.find((c): c is DTable => c.t === "table");
const items = (f: DField) => f.children.filter((c) => c.t === "item");
const rootKeys = (t: DTree) => fields(t.nodes).map((f) => f.key);

describe("parseDescribe: the round trip", () => {
  for (const [name, text] of Object.entries(LAB)) {
    it(`prints the ${name} report back byte for byte`, () => {
      expect(printDescribe(parseDescribe(text))).toBe(text);
    });
  }
  it("keeps trailing blank lines and CRLF line ends", () => {
    for (const text of [`${svc}\n`, `${svc}\n\n\n`, gateway.replace(/\n/g, "\r\n") + "\r\n", "", "\n", "Name:  x"]) {
      expect(printDescribe(parseDescribe(text))).toBe(text);
    }
  });
  it("reads CRLF like LF", () => {
    const t = parseDescribe(gateway.replace(/\n/g, "\r\n"));
    expect(at(t, "Metadata", "UID").value).toBe("69423343-dafb-43d8-a685-f74670ac26a8");
  });
});

describe("parseDescribe: the generic (CRD) describer", () => {
  it("reads the prelude, then API Version, Kind, Metadata, Spec, Status and Events (review 2)", () => {
    const t = parseDescribe(gateway);
    expect(t.generic).toBe(true);
    expect(rootKeys(t)).toEqual(["Name", "Namespace", "Labels", "Annotations", "API Version", "Kind", "Metadata", "Spec", "Status", "Events"]);
    expect(at(t, "Metadata", "UID").value).toBe("69423343-dafb-43d8-a685-f74670ac26a8");
    expect(at(t, "Spec", "Gateway Class Name").value).toBe("lab-envoy");
  });
  it("splits the Gateway's two conditions into two certain items", () => {
    const c = at(parseDescribe(gateway), "Status", "Conditions");
    const its = items(c);
    expect(its).toHaveLength(2);
    expect(its.map((i) => (i.t === "item" ? fields(i.children).find((f) => f.key === "Type")?.value : ""))).toEqual(["Accepted", "Programmed"]);
  });
  it("splits a route's parent conditions, and keeps its one parent whole", () => {
    const t = parseDescribe(route);
    const parents = at(t, "Status", "Parents");
    expect(items(parents)).toHaveLength(0);
    expect(items(at(t, "Status", "Parents", "Conditions"))).toHaveLength(2);
    expect(at(t, "Spec", "Hostnames").children.map((c) => (c.t === "text" ? c.text : c.t))).toEqual(["shop.example.com"]);
  });
  it("reads a one-condition list as fields, and nests the store chain", () => {
    const t = parseDescribe(es);
    expect(items(at(t, "Status", "Conditions"))).toHaveLength(0);
    expect(at(t, "Status", "Conditions", "Reason").value).toBe("SecretSyncedError");
    expect(at(t, "Spec", "Data", "Remote Ref", "Key").value).toBe("grafana-oauth-client-secret");
    expect(at(t, "Spec", "Secret Store Ref", "Name").value).toBe("azure-keyvault-prd");
  });
  it("never splits a list it does not know: [{alpha:1},{beta:2}] and [{alpha:1,beta:2}] read alike (review 1)", () => {
    const text = "Name:  x\nAPI Version:  a/v1\nKind:  K\nMetadata:\n  UID:  u\nSpec:\n  Things:\n    Alpha:  1\n    Beta:   2";
    const t = parseDescribe(text);
    expect(items(at(t, "Spec", "Things"))).toHaveLength(0);
    expect(fields(at(t, "Spec", "Things").children).map((f) => f.key)).toEqual(["Alpha", "Beta"]);
  });
  it("draws no boundary from a label that is not invertible: URL then A, Foo twice (review 2)", () => {
    const text = "Name:  x\nAPI Version:  a/v1\nKind:  K\nMetadata:\n  UID:  u\nSpec:\n  Values:\n    URL:  u\n    A:    1\n    Foo:  2\n    Foo:  3";
    const v = at(parseDescribe(text), "Spec", "Values");
    expect(items(v)).toHaveLength(0);
    expect(fields(v.children).map((f) => f.key)).toEqual(["URL", "A", "Foo", "Foo"]);
  });
  it("splits a known list only for its own kind and path, and only when every key is the list's own (review 3)", () => {
    const own = "Name:  x\nAPI Version:  gateway.networking.k8s.io/v1\nKind:  HTTPRoute\nMetadata:\n  UID:  u\nSpec:\n  Parent Refs:\n    Name:  a\n    Name:  b";
    expect(items(at(parseDescribe(own), "Spec", "Parent Refs"))).toHaveLength(2);
    expect(at(parseDescribe(own), "Spec", "Parent Refs").list).toBe("Parent Refs");
    const foreign = own.replace("    Name:  b", "    Name:  b\n    Weird Key:  c");
    expect(items(at(parseDescribe(foreign), "Spec", "Parent Refs"))).toHaveLength(0);
    // the same property on another kind, or at another path, is not that schema
    const otherKind = own.replace("gateway.networking.k8s.io/v1", "example.com/v1").replace("HTTPRoute", "Widget");
    expect(items(at(parseDescribe(otherKind), "Spec", "Parent Refs"))).toHaveLength(0);
    const otherPath = own.replace("Spec:\n  Parent Refs:", "Spec:\n  Values:\n    Parent Refs:").replace(/\n {4}Name:/g, "\n      Name:");
    expect(items(at(parseDescribe(otherPath), "Spec", "Values", "Parent Refs"))).toHaveLength(0);
  });
  it("does not infer a schema from a property name: a Widget's Rules with Name and name (review 3)", () => {
    const s = "Name:  x\nAPI Version:  example.com/v1\nKind:  Widget\nMetadata:\n  UID:  u\nSpec:\n  Rules:\n    Name:  upper\n    Name:  lower\nEvents:  <none>\n";
    const t = parseDescribe(s);
    expect(printDescribe(t)).toBe(s);
    expect(items(at(t, "Spec", "Rules"))).toHaveLength(0);
    const cond = s.replace("Rules:", "Conditions:").replace("Spec:", "Status:");
    expect(at(parseDescribe(cond), "Status", "Conditions").list).toBeUndefined();
  });
  it("marks a known kind's conditions, and a Gateway listener's own conditions", () => {
    expect(at(parseDescribe(gateway), "Status", "Conditions").list).toBe("conditions");
    expect(at(parseDescribe(ks), "Status", "Conditions").list).toBe("conditions");
  });
  it("keeps a multi-line condition message whose next line reads 'Status: …' inside the message (review 1)", () => {
    const text = [
      "Name:  x", "API Version:  kustomize.toolkit.fluxcd.io/v1", "Kind:  Kustomization", "Metadata:", "  UID:  u",
      "Spec:", "  Path:  ./x", "Status:", "  Conditions:",
      "    Last Transition Time:  2026-10-10T15:30:38Z",
      "    Message:               kustomize build failed:",
      "Status: 404 Not Found",
      "    at ./kustomization.yaml",
      "      nested: field-shaped but deeper than a scalar",
      "    Reason:                BuildFailed",
      "    Status:                False",
      "    Type:                  Ready",
      "Events:  <none>",
    ].join("\n");
    const t = parseDescribe(text);
    expect(printDescribe(t)).toBe(text);
    expect(rootKeys(t)).toEqual(["Name", "API Version", "Kind", "Metadata", "Spec", "Status", "Events"]);
    const msg = at(t, "Status", "Conditions", "Message");
    expect(msg.cont.map((c) => c.text)).toEqual(["Status: 404 Not Found", "    at ./kustomization.yaml", "      nested: field-shaped but deeper than a scalar"]);
    expect(at(t, "Status", "Conditions", "Type").value).toBe("Ready");
  });
  it("never gives a scalar field children in generic mode", () => {
    const text = "Name:  x\nAPI Version:  a/v1\nKind:  K\nMetadata:\n  UID:  u\nSpec:\n  Script:  echo one\n    Inner:  two";
    const s = at(parseDescribe(text), "Spec", "Script");
    expect(s.children).toHaveLength(0);
    expect(s.cont.map((c) => c.text)).toEqual(["    Inner:  two"]);
  });
});

describe("parseDescribe: built-in describers", () => {
  it("reads a Pod: aligned labels, containers, Command as text, the undashed Conditions table, Events", () => {
    const t = parseDescribe(pod);
    expect(t.generic).toBe(false);
    expect(at(t, "Labels").cont.map((c) => c.text)).toEqual(["pod-template-hash=5bfb7b9d46"]);
    expect(at(t, "Containers", "busybox", "Command").children.map((c) => (c.t === "text" ? c.text : c.t))).toEqual(["sh", "-c", "echo boom; exit 1"]);
    expect(at(t, "Containers", "busybox", "State", "Reason").value).toBe("CrashLoopBackOff");
    const cond = tableIn(at(t, "Conditions"))!;
    expect(cond.dashed).toBe(false);
    expect(cond.head).toEqual(["Type", "Status"]);
    expect(cond.rows.map((r) => r.cells)).toContainEqual(["Ready", "False"]);
    expect(at(t, "Tolerations").cont).toHaveLength(1);
    const ev = tableIn(at(t, "Events"))!;
    expect(ev.head).toEqual(["Type", "Reason", "Age", "From", "Message"]);
    expect(ev.rows.map((r) => r.cells.slice(0, 2))).toEqual([["Normal", "Pulled"], ["Warning", "BackOff"]]);
    expect(ev.rows[1].cells[2]).toBe("47s (x4588 over 5d23h)");
  });
  it("reads a Node: conditions, its pods and allocated resources as tables, prose kept", () => {
    const t = parseDescribe(node);
    expect(tableIn(at(t, "Conditions"))!.rows.map((r) => r.cells[0])).toEqual(["MemoryPressure", "DiskPressure", "PIDPressure", "Ready"]);
    const pods = at(t, "Non-terminated Pods");
    expect(pods.value).toBe("(16 in total)");
    expect(tableIn(pods)!.rows).toHaveLength(16);
    expect(tableIn(pods)!.head).toContain("CPU Requests");
    const alloc = at(t, "Allocated resources");
    expect(alloc.children[0]).toMatchObject({ t: "text", text: "(Total limits may be over 100 percent, i.e., overcommitted.)" });
    expect(tableIn(alloc)!.rows.map((r) => r.cells[0])).toEqual(["cpu", "memory", "ephemeral-storage"]);
    expect(at(t, "Annotations").cont).toHaveLength(1);
  });
  it("nests a Deployment's three-space pod template", () => {
    const t = parseDescribe(deploy);
    expect(at(t, "Pod Template", "Containers", "nginx", "Image").value).toBe("nginx:does-not-exist-9");
    expect(at(t, "Pod Template", "Volumes").value).toBe("<none>");
    expect(tableIn(at(t, "Conditions"))!.rows.map((r) => r.cells)).toEqual([
      ["Available", "False", "MinimumReplicasUnavailable"],
      ["Progressing", "False", "ProgressDeadlineExceeded"],
    ]);
  });
  it("keeps colon- and header-shaped Command and Args as text (review 1)", () => {
    const text = [
      "Name:  p", "Containers:", "  app:", "    Command:", "      echo note: value", "      Type   Status", "    Args:", "      --flag: x", "      key: value", "    Image:  busybox",
    ].join("\n");
    const t = parseDescribe(text);
    expect(at(t, "Containers", "app", "Command").children.map((c) => c.t)).toEqual(["text", "text"]);
    expect(at(t, "Containers", "app", "Args").children.map((c) => (c.t === "text" ? c.text : c.t))).toEqual(["--flag: x", "key: value"]);
    expect(at(t, "Containers", "app", "Image").value).toBe("busybox");
  });
  it("keeps a list of scalars as text once it starts", () => {
    const text = "Name:  p\nMounts:\n  /data from vol (rw)\n  note: inside\nQoS Class:  BestEffort";
    const t = parseDescribe(text);
    expect(at(t, "Mounts").children.map((c) => c.t)).toEqual(["text", "text"]);
    expect(at(t, "QoS Class").value).toBe("BestEffort");
  });
  it("keeps a long annotation's indented value with its key", () => {
    const text = "Name:         x\nAnnotations:  a: b\n              long:\n                key: value line\nStatus:       Running";
    const t = parseDescribe(text);
    expect(at(t, "Annotations").cont.map((c) => c.text)).toEqual(["long:", "  key: value line"]);
    expect(at(t, "Annotations").children).toHaveLength(0);
    expect(at(t, "Status").value).toBe("Running");
  });
  it("keeps ConfigMap data opaque: a key's ---- underline is never a table (review 1)", () => {
    const text = [
      "Name:         cm", "Namespace:    default", "Labels:       <none>", "Annotations:  <none>", "", "Data", "====",
      "config.yaml:", "----", "server:", "  port: 8080", "Events:  in the data", "", "", "BinaryData", "====", "", "Events:  <none>",
    ].join("\n");
    const t = parseDescribe(text);
    expect(printDescribe(t)).toBe(text);
    const op = t.nodes.find((n) => n.t === "opaque");
    expect(op).toMatchObject({ t: "opaque", title: "Data" });
    expect(op && op.t === "opaque" ? op.raw.length : 0).toBe(12);
    expect(t.nodes.some((n) => n.t === "table")).toBe(false);
    expect(at(t, "Events").value).toBe("<none>");
  });
  it("finds Events by its table, and keeps a multi-line event message with its row (review 2)", () => {
    const text = [
      "Name:  p",
      "Events:",
      "  Type     Reason   Age   From     Message",
      "  ----     ------   ----  ----     -------",
      "  Warning  Failed   1m    kubelet  first line",
      "Events: retry history follows",
      "  Type  Reason",
      "  ----  ------",
      "  Normal   Pulled   2m    kubelet  pulled",
    ].join("\n");
    const t = parseDescribe(text);
    expect(printDescribe(t)).toBe(text);
    expect(rootKeys(t)).toEqual(["Name", "Events"]);
    const ev = tableIn(at(t, "Events"))!;
    expect(ev.rows.map((r) => r.cells[0])).toEqual(["Warning", "Normal"]);
    expect(ev.rows[0].extra).toEqual(["Events: retry history follows", "  Type  Reason", "  ----  ------"]);
  });
  it("keeps a dashed, header-shaped Command as text (review 3)", () => {
    const s = "Name:  p\nContainers:\n  app:\n    Command:\n      sh\n      Type   Status\n      ----   ------\n      Ready  False\n    Image:  busybox\n";
    const t = parseDescribe(s);
    expect(printDescribe(t)).toBe(s);
    expect(at(t, "Containers", "app", "Command").children.map((c) => c.t)).toEqual(["text", "text", "text", "text"]);
    expect(at(t, "Containers", "app", "Image").value).toBe("busybox");
  });
  it("keeps table-shaped annotation content with its annotation (review 3)", () => {
    const s = "Name:         x\nAnnotations:  note:\n                Type   Status\n                ----   ------\n                Ready  False\nStatus:       Running";
    const t = parseDescribe(s);
    expect(printDescribe(t)).toBe(s);
    expect(at(t, "Annotations").cont).toHaveLength(3);
    expect(at(t, "Annotations").children).toHaveLength(0);
  });
  it("keeps an Events-shaped block inside ConfigMap data opaque, and finds the real Events after BinaryData (review 3)", () => {
    const s = "Name:  cm\nNamespace:  default\n\nData\n====\nconfig.txt:\n----\nEvents:\n  Type     Reason  Message\n  ----     ------  -------\n  Warning  Fake    config content\n\nBinaryData\n====\n\nEvents:  <none>\n";
    const t = parseDescribe(s);
    expect(printDescribe(t)).toBe(s);
    const op = t.nodes.find((n) => n.t === "opaque");
    expect(op && op.t === "opaque" && op.raw.includes("  Warning  Fake    config content")).toBe(true);
    expect(at(t, "Events").value).toBe("<none>");
    expect(t.nodes.some((n) => n.t === "table")).toBe(false);
  });
  it("keeps the suffix opaque when two Events openers cannot be told apart", () => {
    const block = "Events:\n  Type     Reason  Age  From  Message\n  ----     ------  ---  ----  -------\n  Warning  A       1m   k     m";
    const s = `Name:  p\nStatus:  Running\n${block}\n${block}`;
    const t = parseDescribe(s);
    expect(printDescribe(t)).toBe(s);
    expect(rootKeys(t)).toEqual(["Name", "Status"]);
    expect(t.nodes[t.nodes.length - 1]).toMatchObject({ t: "opaque", title: "Events:" });
  });
  it("reads a ragged last column", () => {
    const text = "Events:\n  Type    Reason  Message\n  ----    ------  -------\n  Normal  A       short\n  Normal  B       a much longer message than the header";
    const ev = tableIn(at(parseDescribe(text), "Events"))!;
    expect(ev.rows.map((r) => r.cells[2])).toEqual(["short", "a much longer message than the header"]);
  });
});
