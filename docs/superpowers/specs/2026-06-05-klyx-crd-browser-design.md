# Klyx M4 design (CRD browser grouped by API group)

Date: 2026-06-05
Status: approved design, ready for plan
Scope: M4. A per-cluster custom-resource browser: CRDs grouped by API group with
owning-operator attribution, category badges, scope, and live (lazy, capped)
instance counts. Reference: mockup 5 (`docs/design/mockups-2026-06-04.html` #m5). Architecture:
Approach A (CRD list + lazy metadata counts, pure request/response - no watch).

## 1. Scope, decomposition, placement

M4 is a **per-cluster** browser in the cluster's existing **Resources** section
(breadcrumb `… / <cluster> / Resources / Custom`). Three plans under one spec,
built in order:

- **M4-a** (this spec drives the first plan): the grouped browser - CRD discovery
  -> groups -> kind rows with scope, operator, category badge, and lazy hybrid
  counts. Pure request/response, no informer.
- **M4-b** (later): drill into a kind -> instance list (metadata-only, paginated
  for high-cardinality kinds).
- **M4-c** (later): instance YAML/detail.

M4-a answers "what custom resources exist here, who owns them, how many" - the
differentiator versus the flat alphabetical list other tools give - and it builds
the instance-fetch layer M4-b and M5 (Gateway topology) reuse. Fleet-wide CRD
diffing is a deliberate later enhancement, not M4-a.

### Why no watch (the Cilium point)
Counts require listing (k8s has no count endpoint). Cilium's high-cardinality
CRDs (`CiliumEndpoint` ~1/pod, `CiliumIdentity`) are what make other tools choke:
a standing informer over `CiliumEndpoint` holds thousands of objects in memory
forever. M4-a never watches. Discovery is one cheap list; counts are lazy,
on-demand, capped, and nothing is retained while a group is collapsed.

## 2. Data layer: CRD discovery (`internal/crd`, new pure package)

One dynamic `customresourcedefinitions` list (apiextensions.k8s.io/v1) via the
`dyn` client `ClusterConn` already holds. This single call authoritatively
defines the custom set and carries names/scope/labels. A new pure package
`internal/crd` parses each CRD unstructured:

```go
type Info struct {
    Group      string   // spec.group
    Kind       string   // spec.names.kind
    Plural     string   // spec.names.plural
    ShortNames []string // spec.names.shortNames
    Scope      string   // spec.scope: "Namespaced" | "Cluster"
    Version    string   // storage (else first served) version, for counting
    Operator   string   // best-effort, from metadata.labels (see §4)
}
func ParseCRD(u *unstructured.Unstructured) (Info, bool)
```

`Version` picks `spec.versions[]` where `storage==true`, falling back to the
first entry with `served==true`. `ParseCRD` returns `ok=false` for an
unparseable object (missing group/kind/plural). Pure, unit-tested with fixtures
(namespaced + cluster-scoped, short names present/absent, multi-version storage
pick).

`ClusterConn.ListCRDs(ctx) ([]crd.Info, error)` lists via
`dyn.Resource(crdGVR).List` and maps `ParseCRD` over the items (skipping
`ok=false`). Added to the fleet `Conn` interface + the `fakeConn` stub.
`crdGVR = {apiextensions.k8s.io, v1, customresourcedefinitions}`.

## 3. Counts: hybrid, lazy, capped

```go
const countCap = 500
func (c *ClusterConn) CountResource(ctx context.Context, group, version, plural string) (count int, capped bool, err error)
```

Builds `gvr = {group, version, plural}` and does a metadata-only list via the
`meta` client `ClusterConn` already holds, across all namespaces (works for both
scopes):

```go
list, err := c.meta.Resource(gvr).List(ctx, metav1.ListOptions{Limit: countCap})
// count, capped = countDisplay(len(list.Items), list.GetContinue())
```

The cap->display mapping is a pure helper:
```go
// countDisplay maps a single metadata-list page to a display count. A non-empty
// continue token means there are more than `countCap` items, so report the cap
// as a floor and flag capped.
func countDisplay(items int, continueToken string) (count int, capped bool) {
    if continueToken != "" {
        return countCap, true
    }
    return items, false
}
```

Under the cap -> exact count, `capped=false`. At the cap with a continue token ->
`count=500, capped=true` (UI renders `500+`). `countDisplay` is unit-tested
directly (the fake metadata client does not paginate); `CountResource`'s
uncapped happy path is tested against the metadata fake. Added to `Conn` +
`fakeConn`. **No watch** - one list per kind, fired only on group-expand.

## 4. Attribution (`internal/crd`, pure)

Two best-effort pure functions, fixture-tested:

```go
func Operator(labels map[string]string) string  // first present of a priority list
func Category(group string) string               // curated table by group
```

- **Operator** checks label keys in priority order and returns the first
  non-empty: `app.kubernetes.io/name`, `app.kubernetes.io/part-of`,
  `helm.sh/chart` (chart name with the trailing `-<version>` stripped),
  `app.kubernetes.io/managed-by`. Else `""`. Honest blanks beat wrong guesses.
- **Category** is a curated table keyed by exact group, seeded from the owner's
  stack:

  | group | category |
  |-------|----------|
  | `cilium.io` | CNI |
  | `kustomize.toolkit.fluxcd.io`, `source.toolkit.fluxcd.io`, `helm.toolkit.fluxcd.io`, `notification.toolkit.fluxcd.io` | GITOPS |
  | `argoproj.io` | GITOPS |
  | `cert-manager.io`, `acme.cert-manager.io` | PKI |
  | `gateway.networking.k8s.io` | NETWORK |
  | `gateway.envoyproxy.io` | NETWORK |
  | `external-secrets.io` | SECRETS |
  | `monitoring.coreos.com` | OBSERV |
  | `postgresql.cnpg.io` | DATABASE |

  Unknown group -> `""`. A plain extensible map; new operators add a line.

## 5. appbridge (`CRDService`, request/response)

A new bound `CRDService` registered in `main.go`, no push loop:

```go
type CRDKindDTO struct {
    Kind       string   `json:"kind"`
    Plural     string   `json:"plural"`
    Scope      string   `json:"scope"`
    Version    string   `json:"version"`
    Operator   string   `json:"operator"`
    ShortNames []string `json:"shortNames"`
}
type CRDGroupDTO struct {
    Group    string       `json:"group"`
    Category string       `json:"category"`
    Kinds    []CRDKindDTO `json:"kinds"`
}
type CRDCountDTO struct {
    Count  int  `json:"count"`
    Capped bool `json:"capped"`
}

func (s *CRDService) ListCRDs(cluster string) []CRDGroupDTO
func (s *CRDService) CountKind(cluster, group, version, plural string) CRDCountDTO
```

- `CRDConn` interface (appbridge): `ListCRDs(ctx) ([]crd.Info, error)` +
  `CountResource(ctx, group, version, plural string) (int, bool, error)`. The
  fleet `Conn` satisfies it; the appbridge fake stubs it.
- `ListCRDs(cluster)`: lookup conn -> `conn.ListCRDs` (bounded context) -> group
  by `Group`, attach `crd.Category(group)`, map kinds. Sort groups by name and
  kinds by name (deterministic, stable UI). Empty slice on conn miss or error.
- `CountKind(cluster, group, version, plural)`: lookup conn ->
  `conn.CountResource` (bounded context) -> `CRDCountDTO{count, capped}`. Zero
  value (`{0,false}`) on miss/error. This is the lazy per-kind call the frontend
  fires (concurrently) when a group expands.

DTO shaping (grouping + attribution + sort) is unit-tested with a fake conn
returning a fixed `[]crd.Info`.

## 6. Frontend (`CRDBrowser`)

The cluster's **Resources** section (currently a placeholder) renders
`CRDBrowser`:

- **Summary pill**: `N groups · M kinds · K instances`. `K` accumulates from
  fetched counts and shows `…` until groups are expanded (counts are lazy).
- **group-by toggles**: `api group` (default, native grouping) plus `operator` /
  `scope` / `alphabetical` as pure client-side re-shaping of the same kind list
  (no refetch). `alphabetical` is a flat kind list sorted by kind.
- **Search**: client-side filter over kind / group / operator substring.
- **Group rows**: mono group name, category badge (hidden when `""`), `N kinds`,
  summed instances (`…` until its kinds are counted). Click toggles expand.
- **Kind rows**: Kind + short name, scope badge (`namespaced`/`cluster`), count
  (`…` -> number / `500+`), operator (muted, blank when unknown).
- **Lazy counts**: on group-expand, fire `countKind(cluster, group, version,
  plural)` for each kind in the group concurrently; the store fills counts as
  they resolve.
- **Store `crd` slice**: `groups: CRDGroupDTO[]`, `loading`,
  `expandedGroups: Set<string>`, `counts: Record<string, CRDCountDTO>` keyed
  `"group/version/plural"`, `groupBy`, `search`, plus setters. Cleared on
  cluster change.
- **Bridge `bridge/crd.ts`**: `listCRDs(cluster)` (seeds the slice),
  `countKind(...)` (fills one count).
- Empty state when a cluster has zero CRDs. Reuses existing tokens/badges.

## 7. Testing

- **`internal/crd`**: `ParseCRD` (scope, short names, storage-vs-served version
  pick, reject unparseable); `Operator` (priority order, chart-version strip,
  empty when no known label); `Category` (curated hits + unknown miss);
  `countDisplay` (uncapped exact, capped at cap with a continue token).
- **`internal/fleet`**: `ListCRDs` against the dynamic fake (seeded CRD objects
  across two groups, one with short names, one cluster-scoped) -> assert parsed
  Info set; `CountResource` uncapped happy path against the metadata fake.
- **appbridge**: `ListCRDs` grouping + category attribution + deterministic sort,
  and `CountKind`, both via a fake `CRDConn`.
- **frontend (vitest)**: group/kind render from a mocked bridge; group-by
  re-shaping (operator/scope/alphabetical); search filter; lazy count fill
  (`…` -> number, `500+` when capped); empty state.
- **Playwright smoke + native handoff**: on `homelab-nelli`, the Resources tab
  lists CRD groups; Cilium's group expands fast showing a `500+` on
  endpoints/identities and exact small counts elsewhere; native confirmation is
  the owner's.

## 8. Decisions log

| # | Decision | Rationale |
|---|----------|-----------|
| 1 | All three (browser / instance list / instance detail) as M4-a/b/c under one spec | Ships value incrementally; M4-a builds the instance-fetch layer M4-b and M5 reuse |
| 2 | Approach A: CRD list + lazy metadata counts, no watch | Cheapest; scales to Cilium by construction; a standing CiliumEndpoint informer is the trap to avoid |
| 3 | Hybrid counts: exact under 500, `500+` above | Instant and bounded for every kind; exact where cheap, honest where it isn't |
| 4 | Counts lazy per-group, on-demand, request/response | No memory retained while collapsed; mirrors GetResourceDetail/ResolveGitLink |
| 5 | CRD list (apiextensions) over discovery as the primary source | Authoritatively identifies the custom set AND carries labels for attribution, in one call |
| 6 | Best-effort operator from CRD labels + curated category table | Free (labels ride along); honest blanks beat wrong guesses; table is extensible |
| 7 | Per-cluster; fleet-wide CRD diff deferred | Matches the mockup and the GitOps drilldown pattern |
| 8 | Pure `internal/crd` package for parsing/attribution | Risky/fiddly bits (version pick, label priority, category) become deterministically testable |
