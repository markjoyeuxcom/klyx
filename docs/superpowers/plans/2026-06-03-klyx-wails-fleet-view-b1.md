# Klyx Wails Fleet View — Plan B-1 (toolchain + bridge + live grid)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stand up the Wails v3 native shell and prove the live data path end to end — real `fleet.Registry` snapshots streaming into a React cluster-card grid that updates as clusters change, with light/dark theming. Real kept code, not a throwaway spike.

**Architecture:** A new `internal/appbridge` package (the only Wails-aware Go) projects `fleet.Snapshot` + config tags into `ClusterDTO`, exposes `GetFleet()` to JS, and pushes a `fleet:updated` event on a ~1s coalescing ticker. `cmd/klyx/main.go` boots a Wails v3 app, builds the registry from the Klyx config, and registers the service. The React frontend (Vite + TS + Zustand) seeds from `GetFleet()`, subscribes to `fleet:updated`, and renders a minimal-but-real `ClusterCard` grid with the mockup's theme tokens.

**Tech Stack:** Wails v3 (alpha, pinned), Go 1.22+, client-go; React + TypeScript + Vite + Zustand + @tabler/icons-react.

**Spec:** `docs/superpowers/specs/2026-06-03-klyx-wails-fleet-view-design.md`

**Scope of B-1 (later plans):** B-2 = full card fidelity + chrome + filters/search; B-3 = command palette. B-1 deliberately renders a *minimal* card (name, state, nodes/pods, gitops/network tier, version) — correctness of the live pipe over visual fidelity.

**Note on Wails v3 alpha:** Task 1 installs and scaffolds Wails v3 and is the source of truth for the exact API (service registration, event emit, generated JS bindings). Tasks 4 and 7 (Go event emit) and Task 6 (JS bindings/events import) adapt to what Task 1 pins. Where this plan shows Wails calls, treat them as the intended shape to reconcile against the scaffold; the data shapes (`ClusterDTO`, event name `fleet:updated`) are fixed.

---

### Task 1: Install Wails v3, scaffold, integrate skeleton

Setup/integration task (no unit test — verification is a building app). Establishes the exact Wails v3 API for later tasks.

**Files:**
- Create: `frontend/` (Vite + React + TS app, scaffolded)
- Modify: `cmd/klyx/main.go` (replace later; a Wails bootstrap)
- Modify: `go.mod` / `go.sum` (Wails v3 dep, pinned)
- Create: `Taskfile.yml` or Wails build config as generated
- Create/modify: `.gitignore` (ignore `frontend/node_modules`, `frontend/dist`, `bin/`, `build/`, stray `klyxctl` binary)

- [ ] **Step 1: Install the Wails v3 CLI**

Run:
```bash
go install github.com/wailsapp/wails/v3/cmd/wails3@latest
wails3 version
wails3 doctor
```
Expected: a version prints; `doctor` reports the platform prerequisites (note any missing). Record the exact `wails3` version reported — you will pin it.

- [ ] **Step 2: Scaffold a reference app in /tmp to learn the v3 structure**

Run:
```bash
cd /tmp && rm -rf klyx-wails-ref && wails3 init -n klyx-wails-ref -t react-ts 2>&1 | tail -20 || wails3 init -n klyx-wails-ref 2>&1 | tail -20
ls -R /tmp/klyx-wails-ref | head -60
```
Read the generated `main.go` (or `cmd/*/main.go`), the service/binding example, the `Taskfile.yml`, and the frontend wiring (how JS calls Go and subscribes to events; where generated bindings land — e.g. `frontend/bindings/` or `frontend/wailsjs/`). This is the API reference for Tasks 4, 6, 7.

- [ ] **Step 3: Integrate into the Klyx repo**

Work from `/Users/markjoyeux/Developer/Personal/github/moomora-klyx`. Bring the generated pieces into our existing module (`github.com/moomora/klyx`):
- Copy the generated frontend into `frontend/` (Vite + React + TS).
- Replace `cmd/klyx/main.go` with the Wails bootstrap adapted to our module path (a minimal app that opens one window loading the frontend, with a trivial bound service that returns a string, to prove the round-trip).
- Add the Wails v3 require to `go.mod` PINNED to the exact version from Step 1 (replace any `@latest`). Run `go mod tidy`.
- Add a `Taskfile.yml` (or Make targets) for `dev` and `build` matching the generated config.
- Update `.gitignore`:
  ```
  /frontend/node_modules/
  /frontend/dist/
  /bin/
  /build/bin/
  /klyxctl
  ```

- [ ] **Step 4: Install frontend deps and verify the app builds + dev-serves**

Run:
```bash
cd frontend && npm install && cd ..
wails3 build 2>&1 | tail -20
```
Expected: a binary is produced under `bin/` (or the generated output dir). Then verify dev mode serves the frontend:
```bash
# Start dev in the background; it should print a local Vite URL (e.g. http://localhost:5173 or a wails-assigned port)
( wails3 dev >/tmp/wails-dev.log 2>&1 & echo $! >/tmp/wails-dev.pid ) ; sleep 25 ; grep -iE "vite|localhost|http://" /tmp/wails-dev.log | head
# stop it
kill "$(cat /tmp/wails-dev.pid)" 2>/dev/null || true
```
Expected: the log shows a served local URL. If `wails3 build`/`dev` fails on this machine, STOP and report BLOCKED with the exact error — this is the alpha-toolchain gate the whole plan depends on.

- [ ] **Step 5: Commit**

Stage ONLY the integrated app files (NOT `node_modules`/`dist`/binaries):
```bash
git add cmd/klyx/main.go go.mod go.sum Taskfile.yml .gitignore frontend/ ':!frontend/node_modules' ':!frontend/dist'
git commit -m "$(printf 'chore: scaffold Wails v3 app skeleton (pinned alpha)\n\nCo-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>')"
```

- [ ] **Step 6: Report the pinned Wails version and API specifics**

In your report, record: the pinned Wails v3 version; how a Go service is registered with the app; the exact Go call to emit an event to the frontend; the JS import path + API for (a) calling a bound Go method and (b) subscribing to an event. Tasks 4/6/7 depend on these.

---

### Task 2: Capture the cluster's Kubernetes version

Data-layer addition: `Snapshot.Version`, populated via a one-shot discovery call at connect.

**Files:**
- Modify: `internal/fleet/snapshot.go`
- Modify: `internal/fleet/conn.go`
- Test: `internal/fleet/conn_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/fleet/conn_test.go` (imports `version "k8s.io/apimachinery/pkg/version"` and `discoveryfake "k8s.io/client-go/discovery/fake"` — add if missing):
```go
func TestClusterConnCapturesServerVersion(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	typed := fake.NewSimpleClientset(
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n1"},
			Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}},
	)
	typed.Discovery().(*discoveryfake.FakeDiscovery).FakedServerVersion = &version.Info{GitVersion: "v1.30.4"}

	mscheme := metadatafake.NewTestScheme()
	_ = metav1.AddMetaToScheme(mscheme)
	mclient := metadatafake.NewSimpleMetadataClient(mscheme, podMeta("p1", "default"))

	det := capability.NewDetector(typed)
	c := NewClusterConn("x", typed, mclient, det, clock.Real{})
	c.Start(ctx)

	waitFor(t, 2*time.Second, func() bool {
		s := c.Snapshot()
		return (s.State == Synced || s.State == Degraded) && s.Version == "v1.30.4"
	})
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/fleet/ -run TestClusterConnCapturesServerVersion -v`
Expected: FAIL — `s.Version` undefined.

- [ ] **Step 3: Add the field and capture**

a) In `internal/fleet/snapshot.go`, add a `Version string` field to `Snapshot` (after `Pods int`):
```go
	Version      string
```

b) In `internal/fleet/conn.go`, add a struct field to `ClusterConn` (next to `snapPods int`):
```go
	snapVersion string
```

c) In `connectLoop`, in the `if ok {` branch, after `caps := c.detector.Detect(ctx)` and before storing caps, capture the version:
```go
			ver := ""
			if vi, verr := c.typed.Discovery().ServerVersion(); verr == nil && vi != nil {
				ver = vi.GitVersion
			}

			c.mu.Lock()
			c.caps = caps
			c.snapVersion = ver
			c.mu.Unlock()
```
(Replace the existing `c.mu.Lock(); c.caps = caps; c.mu.Unlock()` block with the above.)

d) In `Snapshot()`, add `Version: c.snapVersion` to the returned struct literal.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/fleet/ -run TestClusterConnCapturesServerVersion -v` then `go test ./internal/fleet/`
Expected: PASS (new test + whole package).

- [ ] **Step 5: Commit**

```bash
git add internal/fleet/snapshot.go internal/fleet/conn.go internal/fleet/conn_test.go
git commit -m "$(printf 'feat: capture Kubernetes server version on Snapshot\n\nCo-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>')"
```

---

### Task 3: `appbridge` ClusterDTO + projection

Pure Go: project a `fleet.Snapshot` + its `config.ClusterConfig` into a JSON-friendly `ClusterDTO`.

**Files:**
- Create: `internal/appbridge/dto.go`
- Test: `internal/appbridge/dto_test.go`

- [ ] **Step 1: Write the failing test**

`internal/appbridge/dto_test.go`:
```go
package appbridge

import (
	"testing"
	"time"

	"github.com/moomora/klyx/internal/capability"
	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/fleet"
)

func TestToDTO(t *testing.T) {
	now := time.Date(2026, 6, 3, 12, 0, 30, 0, time.UTC)
	snap := fleet.Snapshot{
		Name:       "plt-sea-prd-we-aks-01",
		State:      fleet.Synced,
		NodesReady: 12, NodesTotal: 12, Pods: 487,
		Version:  "v1.30.4",
		LastSync: now.Add(-15 * time.Second),
		Capabilities: capability.Set{
			GitOps:  capability.GitOpsCapability{Base: capability.Base{Tier: capability.Healthy}},
			Network: capability.NetworkCapability{Base: capability.Base{Tier: capability.Degraded, Reason: "no EnvoyProxy"}},
		},
	}
	cc := config.ClusterConfig{
		Name:  "plt-sea-prd-we-aks-01",
		Group: "prd-we",
		Tags:  map[string]string{"env": "prd", "region": "we", "provider": "aks"},
	}

	d := ToDTO(snap, cc, now)
	if d.Name != "plt-sea-prd-we-aks-01" {
		t.Fatalf("name: %q", d.Name)
	}
	if d.State != "Synced" {
		t.Fatalf("state: %q", d.State)
	}
	if d.NodesReady != 12 || d.NodesTotal != 12 || d.Pods != 487 {
		t.Fatalf("counts: %+v", d)
	}
	if d.Version != "v1.30.4" {
		t.Fatalf("version: %q", d.Version)
	}
	if d.Env != "prd" || d.Region != "we" || d.Provider != "aks" || d.Group != "prd-we" {
		t.Fatalf("tags: %+v", d)
	}
	if d.GitopsTier != "Healthy" {
		t.Fatalf("gitops tier: %q", d.GitopsTier)
	}
	if d.NetworkTier != "Degraded" || d.NetworkReason != "no EnvoyProxy" {
		t.Fatalf("network: %q/%q", d.NetworkTier, d.NetworkReason)
	}
	if d.AgeSeconds != 15 {
		t.Fatalf("age: %d", d.AgeSeconds)
	}
}

func TestToDTOZeroLastSyncAgeIsZero(t *testing.T) {
	now := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	d := ToDTO(fleet.Snapshot{Name: "x", State: fleet.Connecting}, config.ClusterConfig{Name: "x"}, now)
	if d.AgeSeconds != 0 {
		t.Fatalf("want 0 age when never synced, got %d", d.AgeSeconds)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/appbridge/ -v`
Expected: FAIL — `ToDTO`/`ClusterDTO` undefined.

- [ ] **Step 3: Implement `internal/appbridge/dto.go`**

```go
// Package appbridge is the only Wails-aware Go layer. It projects the pure
// fleet data layer into JSON-friendly DTOs and pushes updates to the frontend.
package appbridge

import (
	"time"

	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/fleet"
)

// ClusterDTO is the JSON-friendly per-cluster shape the frontend consumes.
type ClusterDTO struct {
	Name          string `json:"name"`
	State         string `json:"state"`
	Reason        string `json:"reason"`
	NodesReady    int    `json:"nodesReady"`
	NodesTotal    int    `json:"nodesTotal"`
	Pods          int    `json:"pods"`
	Version       string `json:"version"`
	GitopsTier    string `json:"gitopsTier"`
	GitopsReason  string `json:"gitopsReason"`
	NetworkTier   string `json:"networkTier"`
	NetworkReason string `json:"networkReason"`
	Env           string `json:"env"`
	Region        string `json:"region"`
	Provider      string `json:"provider"`
	Group         string `json:"group"`
	AgeSeconds    int64  `json:"ageSeconds"`
}

// ToDTO projects a snapshot + its config into a ClusterDTO. `now` is injected so
// age is deterministic in tests and consistent across a batch.
func ToDTO(s fleet.Snapshot, cc config.ClusterConfig, now time.Time) ClusterDTO {
	age := int64(0)
	if !s.LastSync.IsZero() {
		age = int64(now.Sub(s.LastSync).Seconds())
		if age < 0 {
			age = 0
		}
	}
	return ClusterDTO{
		Name:          s.Name,
		State:         s.State.String(),
		Reason:        s.Reason,
		NodesReady:    s.NodesReady,
		NodesTotal:    s.NodesTotal,
		Pods:          s.Pods,
		Version:       s.Version,
		GitopsTier:    s.Capabilities.GitOps.Tier.String(),
		GitopsReason:  s.Capabilities.GitOps.Reason,
		NetworkTier:   s.Capabilities.Network.Tier.String(),
		NetworkReason: s.Capabilities.Network.Reason,
		Env:           cc.Tags["env"],
		Region:        cc.Tags["region"],
		Provider:      cc.Tags["provider"],
		Group:         cc.Group,
		AgeSeconds:    age,
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/appbridge/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/appbridge/dto.go internal/appbridge/dto_test.go
git commit -m "$(printf 'feat: appbridge ClusterDTO projection from fleet snapshot + config\n\nCo-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>')"
```

---

### Task 4: `FleetService` (GetFleet + coalescing push loop)

The bound service: builds the DTO list from the registry + config, and pushes `fleet:updated` on a ticker. Event emit adapts to Task 1's API.

**Files:**
- Create: `internal/appbridge/service.go`
- Test: `internal/appbridge/service_test.go`

- [ ] **Step 1: Write the failing test (the pure, Wails-independent core)**

`internal/appbridge/service_test.go`:
```go
package appbridge

import (
	"testing"
	"time"

	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/fleet"
)

// snapshotter is the minimal registry surface the service needs.
type fakeSnapshotter struct{ snaps []fleet.Snapshot }

func (f *fakeSnapshotter) Snapshots() []fleet.Snapshot { return f.snaps }

func TestGetFleetJoinsConfigByName(t *testing.T) {
	now := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	reg := &fakeSnapshotter{snaps: []fleet.Snapshot{
		{Name: "a", State: fleet.Synced},
		{Name: "b", State: fleet.Failed},
	}}
	cfg := &config.Config{Clusters: []config.ClusterConfig{
		{Name: "a", Tags: map[string]string{"env": "prd"}},
		{Name: "b", Tags: map[string]string{"env": "dev"}},
	}}

	svc := NewFleetService(reg, cfg, func() time.Time { return now })
	dtos := svc.GetFleet()

	if len(dtos) != 2 {
		t.Fatalf("want 2 dtos, got %d", len(dtos))
	}
	byName := map[string]ClusterDTO{}
	for _, d := range dtos {
		byName[d.Name] = d
	}
	if byName["a"].Env != "prd" || byName["a"].State != "Synced" {
		t.Fatalf("a wrong: %+v", byName["a"])
	}
	if byName["b"].Env != "dev" || byName["b"].State != "Failed" {
		t.Fatalf("b wrong: %+v", byName["b"])
	}
}

func TestGetFleetUnknownConfigStillProjects(t *testing.T) {
	now := time.Now()
	reg := &fakeSnapshotter{snaps: []fleet.Snapshot{{Name: "ghost", State: fleet.Synced}}}
	cfg := &config.Config{} // no clusters
	svc := NewFleetService(reg, cfg, func() time.Time { return now })
	dtos := svc.GetFleet()
	if len(dtos) != 1 || dtos[0].Name != "ghost" {
		t.Fatalf("want ghost projected with empty tags, got %+v", dtos)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/appbridge/ -run TestGetFleet -v`
Expected: FAIL — `NewFleetService`/`Snapshotter` undefined.

- [ ] **Step 3: Implement the service core `internal/appbridge/service.go`**

```go
package appbridge

import (
	"context"
	"time"

	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/fleet"
)

// Snapshotter is the registry surface the service depends on (so tests can fake it).
type Snapshotter interface {
	Snapshots() []fleet.Snapshot
}

// Emitter pushes a named event with a payload to the frontend. The Wails app
// provides the real implementation; tests provide a fake. (Signature matches the
// Wails v3 event emit reconciled in Task 1.)
type Emitter interface {
	Emit(name string, data any)
}

const FleetUpdatedEvent = "fleet:updated"

// FleetService is bound to JS. GetFleet seeds the UI; Run pushes live updates.
type FleetService struct {
	reg   Snapshotter
	byName map[string]config.ClusterConfig
	now   func() time.Time
}

func NewFleetService(reg Snapshotter, cfg *config.Config, now func() time.Time) *FleetService {
	byName := make(map[string]config.ClusterConfig, len(cfg.Clusters))
	for _, c := range cfg.Clusters {
		byName[c.Name] = c
	}
	return &FleetService{reg: reg, byName: byName, now: now}
}

// GetFleet returns the current fleet as DTOs (bound, callable from JS).
func (s *FleetService) GetFleet() []ClusterDTO {
	snaps := s.reg.Snapshots()
	now := s.now()
	out := make([]ClusterDTO, 0, len(snaps))
	for _, snap := range snaps {
		out = append(out, ToDTO(snap, s.byName[snap.Name], now))
	}
	return out
}

// Run emits FleetUpdatedEvent on a coalescing ticker until ctx is cancelled.
// One emit per tick carries the full current fleet (the frontend replaces state).
func (s *FleetService) Run(ctx context.Context, em Emitter, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			em.Emit(FleetUpdatedEvent, s.GetFleet())
		}
	}
}
```

- [ ] **Step 4: Add a Run test (fake emitter)**

Append to `internal/appbridge/service_test.go`:
```go
import (
	"context"
	"sync"
)

type fakeEmitter struct {
	mu     sync.Mutex
	events int
	last   any
}

func (e *fakeEmitter) Emit(name string, data any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events++
	e.last = data
}

func TestRunEmitsOnTick(t *testing.T) {
	now := time.Now()
	reg := &fakeSnapshotter{snaps: []fleet.Snapshot{{Name: "a", State: fleet.Synced}}}
	svc := NewFleetService(reg, &config.Config{}, func() time.Time { return now })
	em := &fakeEmitter{}

	ctx, cancel := context.WithCancel(context.Background())
	go svc.Run(ctx, em, 10*time.Millisecond)

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		em.mu.Lock()
		n := em.events
		em.mu.Unlock()
		if n >= 1 {
			cancel()
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	t.Fatal("expected at least one emit within 1s")
}
```
(Merge the new imports into the existing import block — do not create a second one.)

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/appbridge/ -race -v`
Expected: PASS (projection + GetFleet + Run), no race.

- [ ] **Step 6: Commit**

```bash
git add internal/appbridge/service.go internal/appbridge/service_test.go
git commit -m "$(printf 'feat: FleetService GetFleet + coalescing fleet:updated push loop\n\nCo-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>')"
```

---

### Task 5: Frontend theme tokens + ThemeProvider

Port the mockup's CSS variables; light/dark with persistence.

**Files:**
- Create: `frontend/src/theme/tokens.css`
- Create: `frontend/src/theme/ThemeProvider.tsx`
- Test: `frontend/src/theme/ThemeProvider.test.tsx`
- Modify: `frontend/src/main.tsx` (import tokens.css; wrap app in ThemeProvider)
- Modify: `frontend/package.json` (add vitest + testing-library devDeps if absent)

- [ ] **Step 1: Add the token CSS (from `docs/design/mockups-2026-06-04.html`)**

`frontend/src/theme/tokens.css` — copy the exact `:root` and `[data-theme="dark"]` variable blocks from `docs/design/mockups-2026-06-04.html` (the `--color-*`, `--font-*`, `--border-radius-*` definitions), plus base rules:
```css
/* tokens.css — ported verbatim from docs/design/mockups-2026-06-04.html */
:root {
  --color-background-primary: #ffffff;
  --color-background-secondary: #f5f4ed;
  --color-background-tertiary: #faf9f5;
  --color-background-info: #e6f1fb;
  --color-background-danger: #fceaea;
  --color-background-success: #eaf3de;
  --color-background-warning: #faeeda;
  --color-text-primary: #1f1e1d;
  --color-text-secondary: #6b6a64;
  --color-text-tertiary: #9c9a92;
  --color-text-info: #185fa5;
  --color-text-danger: #a32d2d;
  --color-text-success: #3b6d11;
  --color-text-warning: #854f0b;
  --color-border-tertiary: rgba(0,0,0,0.10);
  --color-border-secondary: rgba(0,0,0,0.20);
  --color-border-primary: rgba(0,0,0,0.30);
  --color-border-info: #b5d4f4;
  --color-border-danger: #f7c1c1;
  --color-border-success: #c0dd97;
  --color-border-warning: #fac775;
  --font-sans: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
  --font-mono: ui-monospace, SFMono-Regular, "SF Mono", Menlo, Consolas, monospace;
  --border-radius-md: 8px;
  --border-radius-lg: 12px;
}
[data-theme="dark"] {
  --color-background-primary: #262624;
  --color-background-secondary: #2a2926;
  --color-background-tertiary: #1a1918;
  --color-background-info: rgba(24,95,165,0.22);
  --color-background-danger: rgba(163,45,45,0.22);
  --color-background-success: rgba(59,109,17,0.22);
  --color-background-warning: rgba(133,79,11,0.25);
  --color-text-primary: #f5f4ed;
  --color-text-secondary: #b4b2a9;
  --color-text-tertiary: #888780;
  --color-text-info: #85b7eb;
  --color-text-danger: #f09595;
  --color-text-success: #97c459;
  --color-text-warning: #ef9f27;
  --color-border-tertiary: rgba(255,255,255,0.12);
  --color-border-secondary: rgba(255,255,255,0.22);
  --color-border-primary: rgba(255,255,255,0.32);
  --color-border-info: rgba(133,183,235,0.4);
  --color-border-danger: rgba(240,149,149,0.4);
  --color-border-success: rgba(151,196,89,0.4);
  --color-border-warning: rgba(239,159,39,0.4);
}
* { box-sizing: border-box; }
body {
  margin: 0;
  font-family: var(--font-sans);
  background: var(--color-background-tertiary);
  color: var(--color-text-primary);
  font-size: 14px;
  line-height: 1.5;
}
```

- [ ] **Step 2: Write the failing ThemeProvider test**

`frontend/src/theme/ThemeProvider.test.tsx`:
```tsx
import { describe, it, expect, beforeEach } from "vitest";
import { render, act } from "@testing-library/react";
import { ThemeProvider, useTheme } from "./ThemeProvider";

function Toggle() {
  const { theme, toggle } = useTheme();
  return <button onClick={toggle}>{theme}</button>;
}

describe("ThemeProvider", () => {
  beforeEach(() => localStorage.clear());

  it("defaults to light and sets data-theme on the root", () => {
    render(<ThemeProvider><Toggle /></ThemeProvider>);
    expect(document.documentElement.getAttribute("data-theme")).toBe("light");
  });

  it("toggles to dark and persists", () => {
    const { getByRole } = render(<ThemeProvider><Toggle /></ThemeProvider>);
    act(() => getByRole("button").click());
    expect(document.documentElement.getAttribute("data-theme")).toBe("dark");
    expect(localStorage.getItem("klyx-theme")).toBe("dark");
  });
});
```

- [ ] **Step 3: Run test to verify it fails**

Run: `cd frontend && npx vitest run src/theme/ThemeProvider.test.tsx`
Expected: FAIL — module not found / `useTheme` undefined. (If vitest/testing-library aren't installed: `npm i -D vitest @testing-library/react @testing-library/jest-dom jsdom` and add `"test": "vitest"` to package.json scripts plus a `vitest.config.ts` with `environment: "jsdom"`.)

- [ ] **Step 4: Implement `frontend/src/theme/ThemeProvider.tsx`**

```tsx
import { createContext, useContext, useEffect, useState, ReactNode } from "react";

type Theme = "light" | "dark";
type Ctx = { theme: Theme; toggle: () => void };

const ThemeContext = createContext<Ctx | null>(null);

const STORAGE_KEY = "klyx-theme";

function initial(): Theme {
  const saved = localStorage.getItem(STORAGE_KEY);
  return saved === "dark" ? "dark" : "light";
}

export function ThemeProvider({ children }: { children: ReactNode }) {
  const [theme, setTheme] = useState<Theme>(initial);

  useEffect(() => {
    document.documentElement.setAttribute("data-theme", theme);
    localStorage.setItem(STORAGE_KEY, theme);
  }, [theme]);

  const toggle = () => setTheme((t) => (t === "light" ? "dark" : "light"));

  return <ThemeContext.Provider value={{ theme, toggle }}>{children}</ThemeContext.Provider>;
}

export function useTheme(): Ctx {
  const c = useContext(ThemeContext);
  if (!c) throw new Error("useTheme must be used within ThemeProvider");
  return c;
}
```

- [ ] **Step 5: Wire `main.tsx`**

Ensure `frontend/src/main.tsx` imports the tokens and wraps the app:
```tsx
import "./theme/tokens.css";
import { ThemeProvider } from "./theme/ThemeProvider";
// ... inside ReactDOM.createRoot(...).render(
//   <React.StrictMode><ThemeProvider><App /></ThemeProvider></React.StrictMode>
// )
```
(Adapt to the generated `main.tsx` shape from Task 1.)

- [ ] **Step 6: Run test + build**

Run: `cd frontend && npx vitest run src/theme/ && npm run build`
Expected: PASS; frontend builds.

- [ ] **Step 7: Commit**

```bash
git add frontend/src/theme/ frontend/src/main.tsx frontend/package.json frontend/package-lock.json frontend/vitest.config.ts
git commit -m "$(printf 'feat: theme tokens (from mockups) + ThemeProvider with persistence\n\nCo-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>')"
```

---

### Task 6: Bridge subscription + store + minimal ClusterCard grid

Seed from `GetFleet()`, subscribe to `fleet:updated`, render a minimal-but-real card grid. Wails binding/event imports adapt to Task 1.

**Files:**
- Create: `frontend/src/store/fleet.ts` (Zustand store)
- Create: `frontend/src/bridge/fleet.ts` (typed wrappers over the generated bindings + event subscription)
- Create: `frontend/src/fleet/ClusterCard.tsx`
- Create: `frontend/src/fleet/FleetView.tsx`
- Test: `frontend/src/fleet/ClusterCard.test.tsx`
- Modify: `frontend/src/App.tsx` (render FleetView; init the bridge)

- [ ] **Step 1: Define the DTO type + Zustand store**

`frontend/src/store/fleet.ts`:
```ts
import { create } from "zustand";

export type ClusterDTO = {
  name: string;
  state: string;
  reason: string;
  nodesReady: number;
  nodesTotal: number;
  pods: number;
  version: string;
  gitopsTier: string;
  gitopsReason: string;
  networkTier: string;
  networkReason: string;
  env: string;
  region: string;
  provider: string;
  group: string;
  ageSeconds: number;
};

type FleetState = {
  clusters: ClusterDTO[];
  setClusters: (c: ClusterDTO[]) => void;
};

export const useFleet = create<FleetState>((set) => ({
  clusters: [],
  setClusters: (clusters) => set({ clusters }),
}));
```
(Install Zustand: `cd frontend && npm i zustand @tabler/icons-react`.)

- [ ] **Step 2: Bridge module**

`frontend/src/bridge/fleet.ts` — wrap the generated Go binding and the event subscription. Adapt the two imports/calls marked ADAPT to Task 1's pinned API:
```ts
import { useFleet, ClusterDTO } from "../store/fleet";

// ADAPT (Task 1): import the generated GetFleet binding and the runtime Events API.
// e.g. import { GetFleet } from "../../bindings/.../FleetService";
//      import { Events } from "@wailsio/runtime";
import { GetFleet } from "../../bindings/changeme";
import { Events } from "@wailsio/runtime";

const FLEET_UPDATED = "fleet:updated";

// initFleetBridge seeds the store and subscribes to live updates. Returns an
// unsubscribe function.
export async function initFleetBridge(): Promise<() => void> {
  const seed = (await GetFleet()) as ClusterDTO[];
  useFleet.getState().setClusters(seed ?? []);

  // ADAPT (Task 1): exact event subscription signature.
  const off = Events.On(FLEET_UPDATED, (e: { data: ClusterDTO[] }) => {
    useFleet.getState().setClusters(e.data ?? []);
  });
  return typeof off === "function" ? off : () => {};
}
```

- [ ] **Step 3: Write the failing ClusterCard test**

`frontend/src/fleet/ClusterCard.test.tsx`:
```tsx
import { describe, it, expect } from "vitest";
import { render } from "@testing-library/react";
import { ClusterCard } from "./ClusterCard";
import type { ClusterDTO } from "../store/fleet";

const base: ClusterDTO = {
  name: "plt-sea-prd-we-aks-01", state: "Synced", reason: "",
  nodesReady: 12, nodesTotal: 12, pods: 487, version: "v1.30.4",
  gitopsTier: "Healthy", gitopsReason: "", networkTier: "Healthy", networkReason: "",
  env: "prd", region: "we", provider: "aks", group: "prd-we", ageSeconds: 15,
};

describe("ClusterCard", () => {
  it("renders name, version, counts and gitops tier", () => {
    const { getByText } = render(<ClusterCard c={base} />);
    expect(getByText("plt-sea-prd-we-aks-01")).toBeTruthy();
    expect(getByText("v1.30.4")).toBeTruthy();
    expect(getByText("12/12")).toBeTruthy();
    expect(getByText("487")).toBeTruthy();
    expect(getByText(/flux|gitops|healthy/i)).toBeTruthy();
  });

  it("shows the reason for a failed cluster", () => {
    const { getByText } = render(
      <ClusterCard c={{ ...base, state: "Failed", reason: "connect timed out" }} />,
    );
    expect(getByText(/connect timed out/i)).toBeTruthy();
  });
});
```

- [ ] **Step 4: Run test to verify it fails**

Run: `cd frontend && npx vitest run src/fleet/ClusterCard.test.tsx`
Expected: FAIL — `ClusterCard` not found.

- [ ] **Step 5: Implement ClusterCard + FleetView (minimal, themed)**

`frontend/src/fleet/ClusterCard.tsx`:
```tsx
import type { ClusterDTO } from "../store/fleet";

const stateColor: Record<string, string> = {
  Synced: "var(--color-text-success)",
  Degraded: "var(--color-text-warning)",
  Stale: "var(--color-text-warning)",
  Connecting: "var(--color-text-info)",
  Failed: "var(--color-text-danger)",
  Unconnected: "var(--color-text-tertiary)",
};

export function ClusterCard({ c }: { c: ClusterDTO }) {
  return (
    <div style={{
      background: "var(--color-background-primary)",
      border: "0.5px solid var(--color-border-tertiary)",
      borderRadius: "var(--border-radius-md)",
      padding: "10px 12px",
    }}>
      <div style={{ display: "flex", alignItems: "center", gap: 6, marginBottom: 6 }}>
        <span style={{ width: 7, height: 7, borderRadius: "50%", background: stateColor[c.state] ?? "var(--color-text-tertiary)" }} />
        <span style={{ fontFamily: "var(--font-mono)", fontWeight: 500, fontSize: 12 }}>{c.name}</span>
      </div>
      <div style={{ display: "flex", gap: 4, marginBottom: 8, fontSize: 10 }}>
        {c.env && <Badge>{c.env}</Badge>}
        {c.region && <Badge>{c.region}</Badge>}
        {c.version && <Badge>{c.version}</Badge>}
      </div>
      <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 3, fontSize: 11, marginBottom: 8 }}>
        <Stat label="nodes" value={`${c.nodesReady}/${c.nodesTotal}`} />
        <Stat label="pods" value={`${c.pods}`} />
        <Stat label="gitops" value={c.gitopsTier} />
        <Stat label="network" value={c.networkTier} />
      </div>
      <div style={{ borderTop: "0.5px solid var(--color-border-tertiary)", paddingTop: 6, fontSize: 10, color: stateColor[c.state] }}>
        {c.state}{c.reason ? ` — ${c.reason}` : ""}
      </div>
    </div>
  );
}

function Badge({ children }: { children: React.ReactNode }) {
  return <span style={{ background: "var(--color-background-secondary)", color: "var(--color-text-secondary)", padding: "1px 6px", borderRadius: 4 }}>{children}</span>;
}
function Stat({ label, value }: { label: string; value: string }) {
  return <div><span style={{ color: "var(--color-text-tertiary)" }}>{label}</span> <span style={{ fontWeight: 500 }}>{value}</span></div>;
}
```

`frontend/src/fleet/FleetView.tsx`:
```tsx
import { useFleet } from "../store/fleet";
import { ClusterCard } from "./ClusterCard";

export function FleetView() {
  const clusters = useFleet((s) => s.clusters);
  return (
    <div style={{ padding: "14px 16px" }}>
      <div style={{ fontSize: 17, fontWeight: 500, marginBottom: 12 }}>Fleet</div>
      {clusters.length === 0 ? (
        <div style={{ color: "var(--color-text-secondary)", fontSize: 13 }}>No clusters connected yet.</div>
      ) : (
        <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 8 }}>
          {clusters.map((c) => <ClusterCard key={c.name} c={c} />)}
        </div>
      )}
    </div>
  );
}
```

- [ ] **Step 6: Wire App.tsx to init the bridge and render the view**

`frontend/src/App.tsx`:
```tsx
import { useEffect } from "react";
import { FleetView } from "./fleet/FleetView";
import { useTheme } from "./theme/ThemeProvider";
import { initFleetBridge } from "./bridge/fleet";

export default function App() {
  const { theme, toggle } = useTheme();
  useEffect(() => {
    let off = () => {};
    initFleetBridge().then((u) => (off = u)).catch((e) => console.error("bridge init", e));
    return () => off();
  }, []);
  return (
    <div>
      <button onClick={toggle} style={{ position: "fixed", top: 12, right: 12 }}>theme: {theme}</button>
      <FleetView />
    </div>
  );
}
```

- [ ] **Step 7: Run the card tests + build**

Run: `cd frontend && npx vitest run src/fleet/ && npm run build`
Expected: card tests PASS; frontend builds. (The bridge module imports the generated binding; if the build can't resolve it yet, that's wired in Task 7 where the Go service is registered and bindings are generated — for the test run, the ClusterCard test does not import the bridge, so it passes independently.)

- [ ] **Step 8: Commit**

```bash
git add frontend/src/store/ frontend/src/bridge/ frontend/src/fleet/ frontend/src/App.tsx frontend/package.json frontend/package-lock.json
git commit -m "$(printf 'feat: live fleet store, bridge subscription, minimal ClusterCard grid\n\nCo-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>')"
```

---

### Task 7: Wire `main.go` end to end + verify the live pipe

Boot the registry + FleetService in the Wails app; generate bindings; verify live updates via the dev server.

**Files:**
- Modify: `cmd/klyx/main.go`
- Test (manual/Playwright): the running dev server

- [ ] **Step 1: Implement `cmd/klyx/main.go`**

Adapt the Wails v3 bootstrap from Task 1 to: load config (default `~/.config/klyx/fleet.yaml`, override via `KLYX_CONFIG`), build the registry, start it, construct the `FleetService`, register it as a bound service, start `FleetService.Run` in a goroutine with the app's event emitter and a 1s interval, and open the window. Intended shape (reconcile calls with Task 1's API):
```go
package main

import (
	"context"
	"os"
	"time"

	"github.com/moomora/klyx/internal/appbridge"
	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/fleet"
	// ADAPT (Task 1): Wails v3 application package import.
	"github.com/wailsapp/wails/v3/pkg/application"
)

func configPath() string {
	if p := os.Getenv("KLYX_CONFIG"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	return home + "/.config/klyx/fleet.yaml"
}

func main() {
	cfg, err := config.Load(configPath())
	if err != nil {
		panic(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	reg := fleet.NewRegistry(cfg, fleet.DefaultConnFactory(clock.Real{}))
	reg.Start(ctx)

	svc := appbridge.NewFleetService(reg, cfg, time.Now)

	// ADAPT (Task 1): construct the app, register `svc` as a bound service, and
	// obtain the event emitter. The emitter must satisfy appbridge.Emitter
	// (Emit(name string, data any)) — wrap the Wails emit call if the signature
	// differs.
	app := application.New(application.Options{
		Name:     "Klyx",
		Services: []application.Service{application.NewService(svc)},
	})

	em := emitterAdapter{app: app}
	go svc.Run(ctx, em, time.Second)

	app.NewWebviewWindow()
	if err := app.Run(); err != nil {
		panic(err)
	}
}

// emitterAdapter adapts the Wails app event API to appbridge.Emitter.
// ADAPT (Task 1): call the real Wails emit method.
type emitterAdapter struct{ app *application.App }

func (e emitterAdapter) Emit(name string, data any) {
	e.app.EmitEvent(name, data)
}
```

- [ ] **Step 2: Generate bindings and fix the frontend import**

Run the Wails binding generation (per Task 1 — e.g. `wails3 generate bindings` or it runs as part of `wails3 dev`/`build`). Update `frontend/src/bridge/fleet.ts`'s ADAPT import to the actual generated `GetFleet` path and the actual runtime Events import. Run `cd frontend && npm run build` to confirm it resolves.

- [ ] **Step 3: Build the app**

Run: `wails3 build 2>&1 | tail -20`
Expected: builds clean. If the Wails API differs from the intended shape, reconcile `main.go`/`emitterAdapter` against Task 1's notes until it builds.

- [ ] **Step 4: Verify the live pipe headlessly (Playwright against the dev server)**

Prereq: a fleet config the dev machine can reach (the homelab, or a config pointing at a reachable context). Start dev:
```bash
( KLYX_CONFIG="$HOME/.config/klyx/fleet.yaml" wails3 dev >/tmp/wails-dev.log 2>&1 & echo $! >/tmp/wails-dev.pid ) ; sleep 25
grep -iE "localhost|http://" /tmp/wails-dev.log | head
```
Then, using the Playwright/browser tools, open the served URL and assert: at least one `ClusterCard` renders with a cluster name and a state; the theme toggle flips `data-theme` (light↔dark, colors change); and — to prove the live push — scale a controller or otherwise change a cluster (e.g. `kubectl -n flux-system scale deploy/kustomize-controller --replicas=0`) and observe the card's gitops/state value change within a few seconds without reloading. Stop dev: `kill "$(cat /tmp/wails-dev.pid)"`.

- [ ] **Step 5: Hand off the native-shell check to the user**

The native window/packaged binary can't be observed by the agent. In your report, give the user the exact commands to run and what to confirm:
```
wails3 dev    # opens the native window; confirm the fleet grid renders and updates live, and the theme toggle works in both modes
wails3 build  # produces the native binary under bin/
```

- [ ] **Step 6: Commit**

```bash
git add cmd/klyx/main.go frontend/src/bridge/fleet.ts
git commit -m "$(printf 'feat: boot registry + FleetService in Wails app; live fleet:updated pipe\n\nCo-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>')"
```

---

## Self-Review

**Spec coverage (B-1 portion of `2026-06-03-klyx-wails-fleet-view-design.md`):**
- §2 risk-first toolchain spike → Task 1. ✓
- §3 appbridge is only Wails-aware Go; ClusterDTO projection joined by name → Tasks 3-4. ✓
- §3 Snapshot.Version via Discovery().ServerVersion() at connect → Task 2. ✓
- §3 event-push bridge, ~1s coalesce, GetFleet seed → Task 4 (Run + GetFleet), Task 6 (subscribe), Task 7 (wire). ✓
- §4 project structure (cmd/klyx, internal/appbridge, frontend/) → Tasks 1,3,4,5,6. ✓
- §5 theming from mockups, ThemeProvider+persistence, Zustand store, bundled tabler icons, no router → Tasks 5,6. ✓
- §5 store is single source of truth; bridge is only fleet writer → Task 6. ✓
- §6 ClusterCard (minimal in B-1, full in B-2), states incl. Failed/reason → Task 6. ✓ (filters/search/chrome/palette are B-2/B-3, out of B-1 scope — noted in header.)
- §7 verification via Vite dev URL + Playwright; human runs native → Task 7 Steps 4-5. ✓
- §7 testing: appbridge unit tests, ThemeProvider + ClusterCard component tests → Tasks 3,4,5,6. ✓

**Deliberately deferred to B-2/B-3 (not gaps):** full card fidelity, sidebar/header chrome + honest placeholders, filter pills, search, the command palette. Stated in the header.

**Placeholder scan:** the only non-literal parts are the explicitly-marked `ADAPT (Task 1)` Wails-API points (event emit signature, generated-binding import path, application bootstrap) — these are unavoidable for an alpha external framework and are pinned by Task 1, not vague TODOs. All Go-pure and React-pure code is complete.

**Type consistency:** `ClusterDTO` Go fields (Task 3) match the TS `ClusterDTO` (Task 6) one-for-one (camelCase via json tags). `Snapshotter`/`Emitter`/`NewFleetService`/`GetFleet`/`Run`/`FleetUpdatedEvent` (Task 4) are consumed by `main.go` (Task 7). `Snapshot.Version` (Task 2) is read by `ToDTO` (Task 3). `useFleet`/`setClusters` (Task 6 store) are written by the bridge (Task 6) and read by `FleetView` (Task 6). `useTheme`/`ThemeProvider` (Task 5) used by `App.tsx` (Task 6). Event name `fleet:updated` consistent across Task 4 (`FleetUpdatedEvent`) and Task 6 (`FLEET_UPDATED`).
