import { describe, it, expect } from "vitest";
import { BUILTIN_CATALOG } from "./builtins";

describe("BUILTIN_CATALOG sanity", () => {
  const allGvrEntries = BUILTIN_CATALOG.flatMap((cat) =>
    cat.entries.flatMap((e) => e.kind === "gvr" ? [{ ...e.ref, cat: cat.label }] : [])
  );
  const allLensEntries = BUILTIN_CATALOG.flatMap((cat) =>
    cat.entries.flatMap((e) => e.kind === "lens" ? [{ ...e, cat: cat.label }] : [])
  );

  it("every gvr entry has kind, plural, version, group, and scope", () => {
    for (const ref of allGvrEntries) {
      expect(ref.kind, `${ref.cat} entry missing kind`).toBeTruthy();
      expect(ref.plural, `${ref.kind} missing plural`).toBeTruthy();
      expect(ref.version, `${ref.kind} missing version`).toBeTruthy();
      // group may be "" for core group - just check it is defined
      expect(ref.group, `${ref.kind} group is undefined`).toBeDefined();
      expect(ref.scope, `${ref.kind} missing scope`).toBeTruthy();
    }
  });

  it("scope is Namespaced or Cluster for every gvr entry", () => {
    for (const ref of allGvrEntries) {
      expect(["Namespaced", "Cluster"], `${ref.kind} has bad scope "${ref.scope}"`).toContain(ref.scope);
    }
  });

  it("no duplicate plural within the same group (gvr entries)", () => {
    const seen = new Map<string, string>();
    for (const ref of allGvrEntries) {
      const key = `${ref.group}/${ref.plural}`;
      const prev = seen.get(key);
      expect(prev, `duplicate: ${key} (${prev} and ${ref.kind})`).toBeUndefined();
      seen.set(key, ref.kind);
    }
  });

  it("has at least one entry per category", () => {
    for (const cat of BUILTIN_CATALOG) {
      expect(cat.entries.length, `category "${cat.label}" has no entries`).toBeGreaterThan(0);
    }
  });

  it("catalog has exactly 6 categories", () => {
    expect(BUILTIN_CATALOG.length).toBe(6);
  });

	  it("Cluster-scoped resources have the right plurals", () => {
	    const clusterScoped = allGvrEntries.filter((r) => r.scope === "Cluster").map((r) => r.plural);
	    expect(clusterScoped).toContain("namespaces");
	    expect(clusterScoped).toContain("nodes");
	    expect(clusterScoped).toContain("persistentvolumes");
	    expect(clusterScoped).toContain("storageclasses");
	    expect(clusterScoped).toContain("csidrivers");
	    expect(clusterScoped).toContain("csinodes");
	    expect(clusterScoped).toContain("volumeattachments");
	    expect(clusterScoped).toContain("ingressclasses");
	    expect(clusterScoped).toContain("priorityclasses");
	    expect(clusterScoped).toContain("runtimeclasses");
	    expect(clusterScoped).toContain("clusterroles");
	    expect(clusterScoped).toContain("clusterrolebindings");
	  });

	  it("includes the curated missing daily-driver resources without legacy Endpoints", () => {
	    const plurals = allGvrEntries.map((r) => r.plural);
	    expect(plurals).toContain("poddisruptionbudgets");
	    expect(plurals).toContain("leases");
	    expect(plurals).toContain("mutatingwebhookconfigurations");
	    expect(plurals).toContain("validatingwebhookconfigurations");
	    expect(plurals).toContain("validatingadmissionpolicies");
	    expect(plurals).toContain("validatingadmissionpolicybindings");
	    expect(plurals).not.toContain("endpoints");
	  });

  it("no duplicate labels within a category (lens entries)", () => {
    for (const cat of BUILTIN_CATALOG) {
      const lensLabels = cat.entries.flatMap((e) => e.kind === "lens" ? [e.label] : []);
      const unique = new Set(lensLabels);
      expect(unique.size, `duplicate lens label in "${cat.label}"`).toBe(lensLabels.length);
    }
  });

  it("lens entries have a valid section string", () => {
    const validSections = ["overview","gitops","helm","gatewayApi","resources","crds","observability","workloads","pods","events","nodes"];
    for (const entry of allLensEntries) {
      expect(validSections, `"${entry.label}" has unknown section "${entry.section}"`).toContain(entry.section);
    }
  });

  it("Workloads category has lens entries for Deployments, StatefulSets, DaemonSets, Pods first", () => {
    const workloads = BUILTIN_CATALOG.find((c) => c.label === "Workloads")!;
    expect(workloads).toBeDefined();
    // First 4 entries must be lens entries in order
    const first4 = workloads.entries.slice(0, 4);
    expect(first4[0]).toMatchObject({ kind: "lens", label: "Deployments",  section: "workloads" });
    expect(first4[1]).toMatchObject({ kind: "lens", label: "StatefulSets", section: "workloads" });
    expect(first4[2]).toMatchObject({ kind: "lens", label: "DaemonSets",   section: "workloads" });
    expect(first4[3]).toMatchObject({ kind: "lens", label: "Pods",         section: "pods" });
    // Remaining entries are gvr entries (Job, CronJob, ReplicaSet)
    const rest = workloads.entries.slice(4);
    expect(rest.every((e) => e.kind === "gvr")).toBe(true);
  });
});
