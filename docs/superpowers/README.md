# Specs and plans

One spec per feature or slice, with its review revisions and, for recent work, an as-built section; a plan where the work was planned step by step. File names start with the date the document was opened. `docs/backlog.md` holds the living status; `CLAUDE.md` holds the milestone history.

## Foundation and milestones M1-M12 (June-July 2026)

Shipped by v0.9.0 (2026-07-06), except the 2026-07-09 documents (AI Explain, the editable surface), which shipped in v0.10.0.

- [Klyx capability health watch design (live GitOps controller health)](specs/2026-06-03-klyx-capability-health-watch-design.md) (spec)
- [Klyx connection resilience design (bounded connect + watch-driven refresh + self-heal)](specs/2026-06-03-klyx-connection-resilience-design.md) (spec)
- [Klyx foundation design (M1 spine through M6 direction)](specs/2026-06-03-klyx-foundation-design.md) (spec)
- [Klyx Wails fleet view design (native shell + live fleet grid)](specs/2026-06-03-klyx-wails-fleet-view-design.md) (spec)
- [Klyx drill-in navigation design (chrome + cluster detail)](specs/2026-06-04-klyx-drill-in-navigation-design.md) (spec)
- [Klyx GitOps M3-c design (operational actions + Git navigation)](specs/2026-06-04-klyx-gitops-actions-design.md) (spec)
- [Klyx GitOps M3-a design (Flux reconciliation listing)](specs/2026-06-04-klyx-gitops-flux-listing-design.md) (spec)
- [Klyx GitOps M3-b design (resource detail + inventory)](specs/2026-06-04-klyx-gitops-resource-detail-design.md) (spec)
- [Klyx M4 design (CRD browser grouped by API group)](specs/2026-06-05-klyx-crd-browser-design.md) (spec)
- [Klyx M4-c design (CRD instance detail / YAML)](specs/2026-06-05-klyx-crd-instance-detail-design.md) (spec)
- [Klyx M4-b design (CRD instance list)](specs/2026-06-05-klyx-crd-instance-list-design.md) (spec)
- [M5-b-i: Gateway Policy Attachment (Envoy precise) - Design](specs/2026-06-05-klyx-gateway-policies-design.md) (spec)
- [Klyx M5 design (Gateway API topology)](specs/2026-06-05-klyx-gateway-topology-design.md) (spec)
- [M5-b-ii: Cilium Policy Inference (CNP/CCNP, inferred) - Design](specs/2026-06-06-klyx-cilium-policy-inference-design.md) (spec)
- [M5-c: ClusterMesh Edges - Design](specs/2026-06-06-klyx-clustermesh-design.md) (spec)
- [Klyx M7-a: Prometheus metrics foundation - design](specs/2026-06-07-klyx-metrics-foundation-design.md) (spec)
- [Klyx M7-b: Route latency/RPS metrics on the network topology - design](specs/2026-06-07-klyx-route-metrics-design.md) (spec)
- [M7-c-ii-b: Workload CPU/Memory metrics - design](specs/2026-06-08-klyx-workload-metrics-design.md) (spec)
- [Klyx M7-c-ii-a: Workloads health view - design](specs/2026-06-08-klyx-workloads-health-view-design.md) (spec)
- [Klyx M10 design (Flux diagnosis depth)](specs/2026-06-17-klyx-flux-diagnosis-design.md) (spec)
- [Klyx M11 design (cert-manager diagnosis)](specs/2026-06-19-klyx-certmanager-diagnosis-design.md) (spec)
- [Spec: "Explain this" - AI slice 1 (cert-manager issuance chain)](specs/2026-07-09-klyx-ai-explain-certmanager.md) (spec)
- [Klyx Capability Health Watch Implementation Plan](plans/2026-06-03-klyx-capability-health-watch.md) (plan)
- [Klyx Connection Resilience Implementation Plan](plans/2026-06-03-klyx-connection-resilience.md) (plan)
- [Klyx Data Foundation Implementation Plan](plans/2026-06-03-klyx-data-foundation.md) (plan)
- [Klyx Wails Fleet View - Plan B-1 (toolchain + bridge + live grid)](plans/2026-06-03-klyx-wails-fleet-view-b1.md) (plan)
- [Klyx Drill-in Navigation Implementation Plan](plans/2026-06-04-klyx-drill-in-navigation.md) (plan)
- [M3-c-i: GitOps Operational Writes + Guardrail Implementation Plan](plans/2026-06-04-klyx-gitops-actions-writes.md) (plan)
- [Klyx GitOps M3-a Implementation Plan (Flux reconciliation listing)](plans/2026-06-04-klyx-gitops-flux-listing.md) (plan)
- [Klyx GitOps M3-b Implementation Plan (resource detail + inventory)](plans/2026-06-04-klyx-gitops-resource-detail.md) (plan)
- [M3-c-ii: View-in-Git Implementation Plan](plans/2026-06-04-klyx-gitops-view-in-git.md) (plan)
- [M4-a: CRD Browser (grouped by API group) Implementation Plan](plans/2026-06-05-klyx-crd-browser.md) (plan)
- [M4-c: CRD Instance Detail (YAML) Implementation Plan](plans/2026-06-05-klyx-crd-instance-detail.md) (plan)
- [M4-b: CRD Instance List Implementation Plan](plans/2026-06-05-klyx-crd-instance-list.md) (plan)
- [M5-b-i: Gateway Policy Attachment (Envoy precise) Implementation Plan](plans/2026-06-05-klyx-gateway-policies-envoy.md) (plan)
- [M5-a: Gateway Topology - Structural Lane Implementation Plan](plans/2026-06-05-klyx-gateway-topology-lane.md) (plan)
- [M5-b-ii: Cilium Policy Inference (CNP/CCNP) Implementation Plan](plans/2026-06-06-klyx-cilium-policy-inference.md) (plan)
- [M5-c-i: ClusterMesh Fleet Peering Implementation Plan](plans/2026-06-06-klyx-clustermesh-fleet-peering.md) (plan)
- [M5-c-ii: ClusterMesh Topology Arrows (global services) Implementation Plan](plans/2026-06-06-klyx-clustermesh-topology-arrows.md) (plan)
- [M7-a: Prometheus metrics foundation - Implementation Plan](plans/2026-06-07-klyx-metrics-foundation.md) (plan)
- [M7-b: Route latency/RPS metrics - Implementation Plan](plans/2026-06-07-klyx-route-metrics.md) (plan)
- [M7-c-ii-b: Workload CPU/Memory metrics - Implementation Plan](plans/2026-06-08-klyx-workload-metrics.md) (plan)
- [M7-c-ii-a: Workloads health view - Implementation Plan](plans/2026-06-08-klyx-workloads-health-view.md) (plan)
- [Klyx daily-driver roadmap (M9)](plans/2026-06-09-klyx-daily-driver-roadmap.md) (plan)
- [Klyx M10 Implementation Plan (Flux diagnosis depth)](plans/2026-06-17-klyx-flux-diagnosis.md) (plan)
- [Klyx M11 Implementation Plan (cert-manager diagnosis)](plans/2026-06-19-klyx-certmanager-diagnosis.md) (plan)
- [M12: External Secrets Operator diagnosis lens](plans/2026-07-03-klyx-eso-diagnosis.md) (plan)
- [Klyx feature review - editable-everything, ad-hoc deploy, ranked features](plans/2026-07-09-klyx-editable-and-features.md) (plan)

## Redesign and health 1c (2026-10-10 to 10-12)

Shipped in v0.10.0 (2026-10-12).

- [Klyx redesign: the health model (v2)](specs/2026-10-10-klyx-redesign-health-model.md) (spec)
- [Klyx redesign: workspace, inspection and navigation contract](specs/2026-10-10-klyx-redesign-layout-nav.md) (spec)
- [Klyx health slice 1c: the cluster health summary (design addendum, v2.1)](specs/2026-10-11-klyx-health-1c-cluster-summary.md) (spec)
- [Klyx health 1c - slice 3 design: lenses read Observer rows](specs/2026-10-11-klyx-health-1c-slice3-rows.md) (spec)
- [Klyx health 1c, slice 3 addendum: the rows transport after the §6a gate](specs/2026-10-12-klyx-health-1c-slice3-rows-transport.md) (spec)
- [Klyx health 1c, slice 4: the open cluster's mast, tree and Overview on the summary](specs/2026-10-12-klyx-health-1c-slice4-summary.md) (spec)
- [Klyx health 1c, slice 5: the fleet on summaries](specs/2026-10-12-klyx-health-1c-slice5-fleet.md) (spec)
- [Klyx health 1c, slice 6: the final coverage-parity gate](specs/2026-10-12-klyx-health-1c-slice6-parity.md) (spec)

## AI providers, Explain and cluster chat (2026-10-13 to 10-14)

Shipped in v0.11.0 and v0.13.0.

- [Klyx AI: provider choice, settings and Explain in every lens](specs/2026-10-13-klyx-ai-providers-and-explain.md) (spec)
- [Klyx cluster chat: ask about one cluster, read-only](specs/2026-10-14-klyx-cluster-chat.md) (spec)

## The Line (2026-10-14 to 10-16)

Shipped in v0.14.0 (2026-10-16), slices 15 and 16 in v0.15.0.

- [Klyx redesign: The Line](specs/2026-10-14-klyx-the-line-redesign.md) (spec)
- [The Line, slice 2b: the role ledger, transition stream and observation log](specs/2026-10-14-klyx-the-line-slice2b-ledger.md) (spec)
- [The Line, slice 10: health notifications](specs/2026-10-15-klyx-the-line-slice10-notifications.md) (spec)
- [The Line, slice 11: the lens sweep and the station page](specs/2026-10-15-klyx-the-line-slice11-lens-sweep.md) (spec)
- [The Line, slice 12: smoke](specs/2026-10-15-klyx-the-line-slice12-smoke.md) (spec)
- [The Line, slice 3: the line model and LineService](specs/2026-10-15-klyx-the-line-slice3-line-model.md) (spec)
- [The Line, slice 4: chrome](specs/2026-10-15-klyx-the-line-slice4-chrome.md) (spec)
- [The Line, slice 5: the fleet screen](specs/2026-10-15-klyx-the-line-slice5-fleet.md) (spec)
- [The Line, slice 6: Git access](specs/2026-10-15-klyx-the-line-slice6-git.md) (spec)
- [The Line, slice 7: new data, part 2](specs/2026-10-15-klyx-the-line-slice7-data.md) (spec)
- [The Line, slice 8: drawings](specs/2026-10-15-klyx-the-line-slice8-drawings.md) (spec)
- [The Line, slice 9: the fleet metadata index and the console](specs/2026-10-15-klyx-the-line-slice9-index-console.md) (spec)
- [The Line slice 13: design fidelity](specs/2026-10-16-klyx-the-line-slice13-fidelity.md) (spec)
- [The Line slice 14: Describe](specs/2026-10-16-klyx-the-line-slice14-describe.md) (spec)
- [The Line slice 15: the generic browser](specs/2026-10-16-klyx-the-line-slice15-generic-browser.md) (spec)
- [The Line slice 16: the inspector widens and expands](specs/2026-10-16-klyx-the-line-slice16-inspector-width.md) (spec)

## First-class coverage (2026-10-16 to 10-20)

Shipped in v0.15.0 (2026-10-21); the roadmap lists each slice.

- [Klyx first-class coverage: the roadmap](specs/2026-10-16-klyx-coverage-roadmap.md) (spec)
- [Coverage S0b: embedded payloads](specs/2026-10-16-klyx-coverage-s0b-embedded-payloads.md) (spec)
- [Coverage S1a: the Flux Operator](specs/2026-10-16-klyx-coverage-s1a-flux-operator.md) (spec)
- [Coverage S1b: Flux image automation](specs/2026-10-17-klyx-coverage-s1b-image-automation.md) (spec)
- [Coverage S1c: source composition and notification facts](specs/2026-10-17-klyx-coverage-s1c-composition-notification.md) (spec)
- [Coverage S2a: Velero health](specs/2026-10-17-klyx-coverage-s2a-velero-health.md) (spec)
- [Klyx coverage C5: credentials for external endpoints](specs/2026-10-18-klyx-coverage-c5-external-credentials.md) (spec)
- [Coverage S2b: the Velero horizon and lists](specs/2026-10-18-klyx-coverage-s2b-velero-horizon-lists.md) (spec)
- [Coverage S3a: admission backend availability and policy inventory, with C3](specs/2026-10-18-klyx-coverage-s3a-admission-availability.md) (spec)
- [Coverage S4: on-prem load balancing](specs/2026-10-18-klyx-coverage-s4-onprem-loadbalancing.md) (spec)
- [Coverage C2 and S5: management is not health; workload identity](specs/2026-10-18-klyx-coverage-s5-workload-identity.md) (spec)
- [Klyx coverage S6: pods, correct](specs/2026-10-18-klyx-coverage-s6-pods.md) (spec)
- [Klyx coverage S7: Gateway API, current](specs/2026-10-18-klyx-coverage-s7-gateway-api-current.md) (spec)
- [Coverage S8a: alerts, from Alertmanager](specs/2026-10-19-klyx-coverage-s8a-alertmanager.md) (spec)
- [Coverage S8b: log history, from Loki](specs/2026-10-19-klyx-coverage-s8b-loki.md) (spec)
- [Coverage S9: KEDA and autoscaling ownership](specs/2026-10-19-klyx-coverage-s9-keda.md) (spec)
- [Coverage S10: node provisioning, by provider adapter](specs/2026-10-20-klyx-coverage-s10-node-provisioning.md) (spec)
- [Coverage S11a: extended resources](specs/2026-10-20-klyx-coverage-s11a-extended-resources.md) (spec)
- [Coverage S11b: dynamic resource allocation (DRA)](specs/2026-10-20-klyx-coverage-s11b-dra.md) (spec)
- [Coverage S3b: proven admission attribution](specs/2026-10-20-klyx-coverage-s3b-admission-attribution.md) (spec)

## The AI investigations programme (2026-10-20 onward)

The roadmap orders A1, then A2 and A3, then extensions. A1 (deeper investigation) was merged on 2026-10-24, untagged; its spec carries the as-built record and the review. A1.1 (rollout history) and the scored eval, whose first real-model run was A1's smoke, followed the same day.

- [Klyx AI programme: investigation, proactive reports, guarded execution](specs/2026-10-20-klyx-ai-investigations-roadmap.md) (spec)
- [Klyx AI A1: deeper investigation](specs/2026-10-21-klyx-ai-a1-investigation.md) (spec)
- [Klyx AI A1.1: rollout history](specs/2026-10-24-klyx-ai-a1-1-rollout-history.md) (spec)
- [Klyx AI eval: scored investigations on the kind lab](specs/2026-10-22-klyx-ai-eval.md) (spec)
- [Klyx AI A3: guarded execution](specs/2026-10-24-klyx-ai-a3-guarded-execution.md) (spec)

## Settings (2026-10-25 onward)

Claude and Codex reviewed the Settings page and its coverage, independently and then against each other. Slice 0 fixes what the review found wrong; a redesign spec follows, reviewed before it is built, and A2's settings come with it.

- [Klyx Settings slice 0: correctness before the redesign](specs/2026-10-25-klyx-settings-slice0.md) (spec)
- [Klyx Settings: the redesign](specs/2026-10-25-klyx-settings-redesign.md) (spec, for review)

## Next

The extensions research, not yet built.

- [Klyx extensions: research and recommendation](specs/2026-10-16-klyx-extensions-research.md) (spec)
