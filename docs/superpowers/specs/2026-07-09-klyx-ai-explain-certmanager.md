# Spec: "Explain this" — AI slice 1 (cert-manager issuance chain)

Date: 2026-07-09
Status: proposed
Scope: the first AI surface in Klyx. A read-only, on-demand "Explain" affordance in the cert-manager inspector that reasons over the **already-resolved** issuance chain and renders a grounded, citation-backed explanation of why a certificate is stuck. It never fetches raw cluster objects, never sees secret values, and never actuates cluster state.

This slice exists to prove the pattern (grounded reasoning over Klyx's own diagnosis DTOs) and to establish the plumbing every later AI feature reuses: the `internal/ai` transport, the `AIConfig`/backend selection, the egress policy, and the `AIService` shape. Cert-manager is chosen because its chain resolver already computes a structured tree **and** a `failingLink` verdict — the model gets a clean anchor.

## Goal

When a certificate is broken and selected in `CertsView`, the operator clicks **Explain** and gets 2-4 sentences: what is stuck, at which chain node, and what to check next — in correct cert-manager vocabulary, every claim traceable to a node Klyx resolved. The existing deterministic `failureHint()` box stays; AI augments it, never replaces it.

## Non-goals (this slice)

- No cluster mutation, no `cmctl renew`, no Git write. Read-only. (Matches the M11-d ruling: no re-issuance verb.)
- No streaming, no multi-turn chat, no cross-lens synthesis (that is the flagship follow-up).
- No Settings UI for the AI config — it is file-edited in `fleet.yaml` for slice 1 (documented).
- No caching/persistence of explanations beyond the current selection.

## The honesty contract (the load-bearing part)

Klyx's first principle is error-honesty. An AI feature that confabulates cluster state would break the app's soul, so the design makes hallucination structurally hard:

1. **The model's only input is the resolved `Chain`** — never raw objects, never YAML, never secret data. The `certmanager.Chain` DTO carries no secret values by construction (it is kind/name/ready/reason/message per node), so redaction is inherent. The prompt builder additionally asserts (in a test) that the serialized payload contains no `data`/`stringData`/`<masked>` keys — belt and suspenders.
2. **Anchor on `failingLink`.** The server already computed the deepest stuck node (`certmanager.FirstFailingLink`, `internal/certmanager/chain.go:99`). The prompt hands the model the whole tree plus this pre-computed verdict, so the explanation is anchored to Klyx's own conclusion, not free-floating.
3. **Cite or abstain.** The system prompt requires the model to name the node (kind + name) its conclusion rests on, and to answer "insufficient data to determine a cause" rather than invent objects/conditions/fixes not present in the input.
4. **Distinguish `Failed` from `Issuing`.** `FirstFailingLink` flags both broken (`Failed`) and in-flight (`Issuing`) nodes. The prompt tells the model an `Issuing` link means "actively working, not yet failed" so it does not report a healthy in-progress issuance as a fault.
5. **Traceability in the UI.** The result DTO returns `groundedOn` — the node identities that were sent (e.g. `Certificate/ollama-tls, Issuer/le-prod, Challenge/abc123`). The panel shows "Grounded on: …" so the engineer can cross-check against the chain rail right above it.
6. **Advisory framing.** The panel footer reads "AI-generated from the resolved chain — verify before acting." AI is never presented as authoritative.

## Egress policy (the second sensitive axis)

Even without secrets, sending cluster topology (cert/issuer/challenge names, reasons) to a hosted LLM is an egress event. Reuse the existing protected-cluster discipline:

- AI is **opt-in and off by default** (`AIConfig.Backend: "off"`). The Explain button renders only when `AIService.Available()` reports a configured backend.
- Two backends: `hosted` (Anthropic Messages API) and `local` (OpenAI-compatible `/v1/chat/completions`, i.e. an Ollama / llama.cpp server on the owner's ROCm box). Local means **nothing leaves the machine** — always permitted.
- **Protected clusters + hosted backend**: refused unless `AIConfig.AllowProtectedEgress: true`. On a protected cluster with a hosted backend and egress not allowed, the button is disabled with a tooltip. Protection is read live via `configSvc.IsProtected(cluster)` (mirrors `ProtectGuard`).
- **Audit trail**: every Explain call logs an Activity-log entry (`Explain requested — Certificate/<ns>/<name> on <cluster> via <backend>`), reusing `setActionStatus`. Egress is never silent.

This is read-only, so it does **not** consume a `ProtectGuard` grant (that gates mutations). The egress gate is its own check inside `AIService`.

## Architecture

```
CertsView / CertInspector  ── Explain button (gated on Available() + egress policy)
        │  explainCertificate(cluster, ns, name)         [bridge]
        ▼
AIService.ExplainCertificate(cluster, ns, name) -> AIResultDTO   [internal/appbridge]
        │  1. availability + egress gate
        │  2. conn.GetCertificateChain(ctx, ns, name)    [re-resolve fresh, server-authoritative]
        │  3. ai.BuildCertExplainPrompt(chain)           [internal/ai — grounded, no secrets]
        │  4. client.Complete(ctx, prompt)               [internal/ai transport]
        ▼
internal/ai.Client  ──HTTP──▶  Anthropic Messages  |  local OpenAI-compat endpoint
```

Data flows one way; the model only ever receives the structured chain the server just resolved.

## Backend

### `internal/ai` (new pure package)

Mirrors `internal/metrics/transport.go`. No Wails imports.

- `type Backend string` — `"hosted" | "local"`.
- `type Client struct { backend Backend; baseURL, apiKey, model string; http *http.Client }`
- `func NewClient(backend Backend, baseURL, apiKey, model string, http *http.Client) *Client` — caller builds the `*http.Client` with the timeout (LLM latency: default 60s) and `InsecureSkipVerify` for a local self-signed endpoint, exactly as `fleet/metrics.go:55` does for Prometheus.
- `func (c *Client) Complete(ctx context.Context, system, user string, maxTokens int) (string, error)` — builds the request per backend, sets Bearer/`x-api-key` auth, `io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))` (4 MiB cap, per `transport.go:15`), decodes the completion text, returns it. Context owns the deadline (no internal timeout), same as `directTransport`.
  - Hosted adapter: `POST {baseURL}/v1/messages`, headers `x-api-key`, `anthropic-version: 2023-06-01`, body `{model, max_tokens, system, messages:[{role:"user",content:user}]}`, parse `content[0].text`.
  - Local adapter: `POST {baseURL}/v1/chat/completions`, optional `Authorization: Bearer`, body `{model, messages:[{role:"system"...},{role:"user"...}], max_tokens, temperature:0}`, parse `choices[0].message.content`.
- `func BuildCertExplainPrompt(chain certmanager.Chain) (system, user string)` — the grounded prompt builder (below). Pure and unit-testable; the redaction assertion lives here.

**Prompt (system):**
> You are a Kubernetes cert-manager diagnostics assistant embedded in Klyx. You are given the ALREADY-RESOLVED issuance chain for one Certificate as structured JSON — ground truth extracted from the live cluster by Klyx. In 2-4 sentences, explain why issuance is stuck and what the operator should check next. Rules: reason ONLY from the provided chain; name the specific node (kind + name) your conclusion rests on; a node with ready="Failed" is broken, ready="Issuing" is in-flight and NOT yet a failure; if every node is Ready, say issuance is healthy; if the data is insufficient to determine a cause, say exactly that — never invent objects, conditions, or fixes not supported by the input. Klyx is read-only and desired state is authored in Git — suggest what to change, never imply Klyx will apply it. Use cert-manager vocabulary correctly.

**Prompt (user):** the chain serialized to compact JSON, with a one-line note: `Klyx's computed deepest-stuck node (failingLink): <kind>/<name> or "none — chain is Ready"`.

`maxTokens: 512`, temperature 0 (determinism).

### `internal/config`

Add a global AI section (AI settings are not per-cluster):

```go
type AIConfig struct {
	Backend              string `yaml:"backend"`              // "off" (default) | "hosted" | "local"
	BaseURL              string `yaml:"baseUrl"`              // hosted: https://api.anthropic.com ; local: http://localhost:11434
	APIKey               string `yaml:"apiKey"`
	Model                string `yaml:"model"`                // hosted default "claude-sonnet-5"; local e.g. "llama3.1"
	AllowProtectedEgress bool   `yaml:"allowProtectedEgress"` // default false: hosted backend refused on protected clusters
	TLSSkipVerify        bool   `yaml:"tlsSkipVerify"`         // for a local self-signed endpoint
}
```

- `Config` gains `AI *AIConfig \`yaml:"ai"\``. `Load` reads it for free; `applyDefaults` sets `Backend:"off"` when `AI` is nil and fills the hosted/local `BaseURL`/`Model` defaults by backend.
- Add `"ai"` to `reservedTagKeys` (`config.go:90`) so a mistyped top-level key warns.
- Not written by `writeClusterEntry` (that path only rewrites cluster entries; AI is global) — file-edited for slice 1. A Settings pane is a follow-up.

### `internal/appbridge` — `AIService`

New `ai_service.go` + `ai_dto.go`, following the `CertManagerService` shape (`certmanager_service.go:50`).

```go
// CertExplainConn is the narrow per-cluster surface AIService needs — the same
// on-demand resolver CertManagerConn already exposes.
type CertExplainConn interface {
	GetCertificateChain(ctx context.Context, ns, name string) (certmanager.Chain, error)
}

type AIService struct {
	lookup      func(string) (CertExplainConn, bool)
	client      *ai.Client         // nil when Backend=="off"
	backend     string
	allowEgress bool               // AllowProtectedEgress
	isProtected func(string) bool  // configSvc.IsProtected
}

func NewAIService(lookup func(string) (CertExplainConn, bool), client *ai.Client, backend string, allowEgress bool, isProtected func(string) bool) *AIService

// Available tells the frontend whether to render the Explain affordance at all.
func (s *AIService) Available() AIAvailabilityDTO   // { backend string, ready bool }

// EgressAllowed reports whether an Explain call would be permitted for this
// cluster (false = protected + hosted + egress not allowed), so the UI can
// disable-with-tooltip rather than fail on click.
func (s *AIService) EgressAllowed(cluster string) bool

// ExplainCertificate resolves the chain fresh, builds the grounded prompt, calls
// the model, and returns the explanation. Read-only. Errors fold into the DTO
// (never throws), mirroring FluxDiff/GetCertificateChain.
func (s *AIService) ExplainCertificate(cluster, ns, name string) AIResultDTO
```

`ExplainCertificate` flow:
1. `client == nil` → `AIResultDTO{Error: "AI backend not configured"}`.
2. `backend == "hosted" && isProtected(cluster) && !allowEgress` → `AIResultDTO{Error: "AI is disabled for protected cluster \"<c>\": use a local backend or set ai.allowProtectedEgress"}`.
3. `conn, ok := lookup(cluster)`; miss → error DTO.
4. `chain, err := conn.GetCertificateChain(ctx, ns, name)` under `context.WithTimeout(60s)`; err → error DTO.
5. `system, user := ai.BuildCertExplainPrompt(chain)`; `text, err := s.client.Complete(ctx, system, user, 512)`; err → error DTO.
6. Return `AIResultDTO{OK:true, Explanation:text, Model:model, Backend:backend, GroundedOn: nodeIdentities(chain)}`.

```go
type AIResultDTO struct {
	OK          bool   `json:"ok"`
	Explanation string `json:"explanation"`
	Model       string `json:"model"`
	Backend     string `json:"backend"`
	GroundedOn  string `json:"groundedOn"` // "Certificate/ns/name, Issuer/le-prod, Challenge/abc (failingLink)"
	Error       string `json:"error"`
}
type AIAvailabilityDTO struct {
	Backend string `json:"backend"`
	Ready   bool   `json:"ready"`
}
```

### `cmd/klyx/main.go`

Build near the other services (~`main.go:100`):
```go
var aiClient *ai.Client
if cfg.AI != nil && cfg.AI.Backend != "off" && cfg.AI.Backend != "" {
	httpc := &http.Client{Timeout: aiHTTPTimeout} // 60s; TLSSkipVerify per cfg.AI.TLSSkipVerify
	aiClient = ai.NewClient(ai.Backend(cfg.AI.Backend), cfg.AI.BaseURL, cfg.AI.APIKey, cfg.AI.Model, httpc)
}
aiSvc := appbridge.NewAIService(
	func(name string) (appbridge.CertExplainConn, bool) { c, ok := reg.Conn(name); if !ok { return nil, false }; return c, true },
	aiClient, aiBackend, aiAllowEgress, configSvc.IsProtected,
)
```
Register `application.NewService(aiSvc)` in the `Services` slice (`main.go:352`).

## Frontend

### `store/fleet.ts`
- `AIResultDTO` / `AIAvailabilityDTO` TS types matching the Go DTOs.
- A tiny `ai` slice OR local `CertInspector` state — slice 1 uses local component state keyed by cert (`{explanation, groundedOn, model, loading, error}`), reset on selection change. `available: AIAvailabilityDTO` fetched once on mount (from `AIService.Available()`), plus a per-cluster `egressAllowed` check.
- Bridge wrappers in `bridge/ai.ts`: `aiAvailable()`, `aiEgressAllowed(cluster)`, `explainCertificate(cluster, ns, name)`.

### `cluster/CertsView.tsx` — `CertInspector`
- An **Explain** button (Tabler `IconSparkles`) in the header row next to `CopyButton` (`CertsView.tsx:356`), OR as an `InspectorSection` between the `failureHint` box (`:374`) and the `issuerBroken` headline (`:377`). Rendered only when `available.ready`; disabled with a tooltip when `!egressAllowed`.
- On click → `explainCertificate` → loading state mirroring the `…resolving chain…` spinner (`CertsView.tsx:396`) → render the result in an `InspectorSection` titled **"Explain (AI)"**:
  - the `explanation` text (a data well),
  - a mono footer line `Grounded on: {groundedOn} · {model}`,
  - the disclaimer "AI-generated from the resolved chain — verify before acting."
- Errors → inline message in the section **and** a toast via `setActionStatus({kind:"error", message})`.
- On success, log an audit line to the Activity log: `setActionStatus({kind:"success", message: "AI explain — Certificate/<ns>/<name> via <backend>"})`.
- The deterministic `failureHint()` box stays above it — AI is the optional deeper read, not a replacement.

The AI panel is data in an inset well (`InspectorSection`) — Signal Bench compliant ("wells hold data").

## Testing

Locally runnable here (per `local-toolchain-runnable`): Go `-race`, tsc, vitest. A real LLM round-trip needs a key → manual smoke test (eyeball item).

**Go**
- `internal/ai/transport_test.go` — `httptest.Server` per backend: assert request path, auth header, body shape, the `LimitReader` cap, and completion-text extraction; a non-2xx returns an error.
- `internal/ai/prompt_test.go` — given a `certmanager.Chain` with a failing `Challenge`, assert the user payload contains `failingLink` and the node identities, and — the redaction guard — contains no `data`/`stringData`/`<masked>` substrings and no secret values.
- `internal/appbridge/ai_service_test.go` — fake `ai.Client` (injectable) + fake `CertExplainConn`: (a) `Backend:"off"`/nil client → `Available().ready==false`, `ExplainCertificate` returns the not-configured error; (b) protected + hosted + `!allowEgress` → `EgressAllowed==false`, `ExplainCertificate` returns the egress-refused error and **never calls the client**; (c) local backend on a protected cluster → allowed; (d) happy path → `OK`, `Explanation`, `GroundedOn` populated; (e) resolver error and client error → `Error` set, no panic.

**Frontend**
- `CertsView.test.tsx` — Explain button hidden when `available.ready` false; disabled+tooltip when `egressAllowed` false; click → loading → renders explanation + "Grounded on" + disclaimer; error path → inline + toast (mock `bridge/ai`).

## File-by-file change list

- `internal/ai/transport.go`, `prompt.go`, `transport_test.go`, `prompt_test.go` (new).
- `internal/config/config.go` — `AIConfig`, `Config.AI`, defaults, `reservedTagKeys`.
- `internal/appbridge/ai_service.go`, `ai_dto.go`, `ai_service_test.go` (new).
- `cmd/klyx/main.go` — build `aiClient` + `aiSvc`, register in `Services`.
- `cmd/klyx/frontend/src/store/fleet.ts` — DTO types + ai state.
- `cmd/klyx/frontend/src/bridge/ai.ts` (new).
- `cmd/klyx/frontend/src/cluster/CertsView.tsx` — Explain affordance + panel; `CertsView.test.tsx`.
- Docs: this spec; a one-line `fleet.yaml` `ai:` example in the README/config docs.

## Stress-tests

- **Hallucination.** Input is the resolved chain only; prompt requires cite-or-abstain and marks the failingLink; a redaction test proves no secrets in the payload; the deterministic `failureHint` stays visible. If the model still overreaches, the "Grounded on" line + the chain rail directly above make it falsifiable at a glance. Residual risk: the model could misread a correct chain — acceptable because it is advisory and side-by-side with the ground truth.
- **Egress.** Off by default; hosted refused on protected clusters unless explicitly allowed; local keeps everything on the ROCm box; every call is audit-logged. The one gap: `allowProtectedEgress` is a blunt global toggle — fine for a single-user desktop; a per-cluster override is a follow-up if needed.
- **Cost/latency.** On-demand and user-triggered only (never per-render — informer-app rule). `max_tokens:512`, one call per click. A 60s timeout covers slow local models.
- **Failure modes.** Every error folds into `AIResultDTO.Error` (no throws), matching `FluxDiff`. Missing key / unreachable endpoint / non-2xx / cluster miss / resolve error all render as an inline message + toast, never a crash or a fake answer.
- **Trust.** Platform engineers distrust magic; trust here comes from traceability (grounded-on), the always-present deterministic hint, and read-only-ness — the model can be wrong without being dangerous.

## Deferred / follow-ups (the pattern generalizes)

- Extend the same `AIService` harness to the flagship **cross-lens root-cause synthesis** (walk Flux + Gateway + cert + ESO chains together).
- **Draft-the-fix-to-Git** (the actuation-safe writer) once Explain is trusted.
- Streaming responses; per-cert result caching; a Settings pane for the AI config (replaces file-editing); a per-cluster egress override; response-quality evals.
- Local-model UX polish (model discovery against the Ollama endpoint).
