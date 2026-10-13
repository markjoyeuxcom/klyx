import { useEffect, useId, useRef, useState } from "react";
import { IconAlertTriangle } from "@tabler/icons-react";
import { ConfirmDialog } from "../chrome/ConfirmDialog";
import {
  AI_EGRESS_PHRASE,
  armAIEgress,
  clearAPIKey,
  getAISettings,
  listAIModels,
  moveKeyToKeychain,
  saveAISettings,
  setAPIKey,
  testAIProvider,
  type AISettings,
  type AISettingsState,
  type AITest,
  type AIModel,
} from "../bridge/aiSettings";

// AISettingsPanel is Settings > AI (spec 2026-10-13 §2.4). It edits a draft of
// fleet.yaml's ai block and shows exactly where prompts go. A key goes in
// through the keychain and never comes back to the page. Every change is
// applied by the backend only after the provider builds and fleet.yaml is
// written, so what the status line says is what Explain uses.

export const PROVIDERS: { id: string; label: string; keyed: boolean; defaultOrigin?: string }[] = [
  { id: "off", label: "Off", keyed: false },
  { id: "anthropic", label: "Claude (Anthropic API key)", keyed: true, defaultOrigin: "https://api.anthropic.com" },
  { id: "openai", label: "OpenAI (API key)", keyed: true, defaultOrigin: "https://api.openai.com" },
  { id: "gemini", label: "Gemini (API key)", keyed: true, defaultOrigin: "https://generativelanguage.googleapis.com" },
  { id: "openaiCompatible", label: "Local server (OpenAI-compatible, e.g. Ollama)", keyed: true },
];

export function providerLabel(id: string): string {
  return PROVIDERS.find((p) => p.id === id)?.label ?? id;
}

const row: React.CSSProperties = { display: "flex", alignItems: "center", gap: 8, padding: "4px 0" };
const labelStyle: React.CSSProperties = { fontSize: 10, color: "var(--color-text-tertiary)", width: 110, flexShrink: 0 };
const hint: React.CSSProperties = { fontSize: 11, color: "var(--color-text-tertiary)" };

export function AISettingsPanel() {
  const [state, setState] = useState<AISettingsState | null>(null);
  const [draft, setDraft] = useState<AISettings | null>(null);
  const [keyInput, setKeyInput] = useState("");
  const [models, setModels] = useState<AIModel[]>([]);
  const [message, setMessage] = useState<{ kind: "ok" | "error"; text: string } | null>(null);
  const [test, setTest] = useState<AITest | null>(null);
  const [busy, setBusy] = useState(false);
  const [confirmEgress, setConfirmEgress] = useState(false);
  const [loadFailed, setLoadFailed] = useState(false);
  const modelListId = useId();
  // Draft generation: every draft edit bumps it, and a model list or Test
  // reply for an older generation is dropped - a late reply for the previous
  // provider or origin must never land against the current draft (Codex
  // review of c488e34).
  const draftGen = useRef(0);

  const load = async (keepDraft = false) => {
    const s = await getAISettings();
    setLoadFailed(s === null);
    if (!s) return;
    setState(s);
    if (!keepDraft || !draft) setDraft(s.persisted);
  };
  useEffect(() => {
    void load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  if (loadFailed && !state) {
    return (
      <div data-testid="ai-load-failed" style={{ ...hint, color: "var(--color-text-danger)" }}>
        Could not read the AI settings.{" "}
        <button className="l-btn" onClick={() => void load()}>Retry</button>
      </div>
    );
  }
  if (!state || !draft) return <div style={hint}>Reading AI settings…</div>;

  const provider = PROVIDERS.find((p) => p.id === draft.provider) ?? PROVIDERS[0];
  const keyStored = !!state.keysStored[draft.provider];
  const run = async (fn: () => Promise<{ ok: boolean; error: string }>, okText: string, keepDraft = false) => {
    setBusy(true);
    const r = await fn();
    setBusy(false);
    setMessage(r.ok ? { kind: "ok", text: okText } : { kind: "error", text: r.error || "failed" });
    if (r.ok) await load(keepDraft);
  };
  const update = (patch: Partial<AISettings>) => {
    draftGen.current++;
    setDraft({ ...draft, ...patch });
    setTest(null);
    // A list belongs to one provider, origin and key: any change voids it.
    if ("provider" in patch || "baseUrl" in patch || "keySource" in patch || "apiKeyEnv" in patch) setModels([]);
  };
  const chooseProvider = (id: string) => {
    const same = id === state.persisted.provider;
    setModels([]);
    update({
      provider: id,
      model: same ? state.persisted.model : "",
      baseUrl: same ? state.persisted.baseUrl : id === "openaiCompatible" ? "http://localhost:11434" : "",
      keySource: same ? state.persisted.keySource : PROVIDERS.find((p) => p.id === id)?.keyed ? "keychain" : "none",
      tlsSkipVerify: same ? state.persisted.tlsSkipVerify : false,
    });
  };
  const save = (d: AISettings) => run(() => saveAISettings(d), "Saved. Explain and Suggest-fix now use these settings.");

  const a = state.active;
  const customOrigin = provider.defaultOrigin && draft.baseUrl ? draft.baseUrl : "";

  return (
    <div data-testid="ai-settings">
      <div data-testid="ai-status" role="status" style={{ fontSize: 12, marginBottom: 8, color: a.problem ? "var(--color-text-warning)" : "var(--color-text-primary)" }}>
        {a.backend === "off" || !a.backend
          ? "AI is off. Choose a provider to enable Explain and Suggest-fix."
          : a.ready
            ? <>Prompts go to <span style={{ fontFamily: "var(--font-mono)" }}>{a.origin}</span> · {providerLabel(a.backend)} · <span style={{ fontFamily: "var(--font-mono)" }}>{a.model}</span></>
            : <>AI is unavailable: {a.problem}</>}
      </div>

      {state.diverged && (
        <Notice testId="ai-diverged">The AI settings in fleet.yaml differ from the ones in use. Save to apply them, or check the file.</Notice>
      )}
      {state.persistedError && <Notice testId="ai-persisted-error">fleet.yaml cannot be read: {state.persistedError}</Notice>}
      {state.keychainError && <Notice testId="ai-keychain-error">{state.keychainError}</Notice>}
      {state.legacyKeyInFile && (
        <Notice testId="ai-legacy-key">
          Your API key is stored in fleet.yaml in plain text.{" "}
          <button className="l-btn" disabled={busy} onClick={() => void run(moveKeyToKeychain, "Key moved to the keychain and removed from fleet.yaml.")}>Move to keychain</button>
        </Notice>
      )}

      <div style={row}>
        <span style={labelStyle}>provider</span>
        <select aria-label="AI provider" className="l-input" value={draft.provider} onChange={(e) => chooseProvider(e.target.value)}>
          {PROVIDERS.map((p) => <option key={p.id} value={p.id}>{p.label}</option>)}
        </select>
      </div>
      <div style={{ ...hint, marginLeft: 118, marginBottom: 4 }}>Installed CLIs (Claude Code) arrive in a later update.</div>

      {provider.id !== "off" && (
        <>
          {provider.keyed && (
            <>
              <div style={row}>
                <span style={labelStyle}>key from</span>
                <select aria-label="Key source" className="l-input" value={draft.keySource === "legacy" ? "legacy" : draft.keySource} onChange={(e) => update({ keySource: e.target.value })}>
                  <option value="keychain">Keychain</option>
                  <option value="env">Environment variable</option>
                  {provider.id === "openaiCompatible" && <option value="none">No key</option>}
                  {draft.keySource === "legacy" && <option value="legacy">fleet.yaml (plain text)</option>}
                </select>
                {draft.keySource === "env" && (
                  <input aria-label="Environment variable name" className="l-input" placeholder="ANTHROPIC_API_KEY" value={draft.apiKeyEnv} onChange={(e) => update({ apiKeyEnv: e.target.value })} />
                )}
              </div>
              {draft.keySource === "keychain" && (
                <div style={row}>
                  <span style={labelStyle}>API key</span>
                  <span data-testid="ai-key-state" style={{ fontSize: 11, color: keyStored ? "var(--color-text-primary)" : "var(--color-text-tertiary)" }}>
                    {keyStored ? "stored in the keychain" : "no key stored"}
                  </span>
                  <input
                    aria-label="API key"
                    type="password"
                    autoComplete="off"
                    spellCheck={false}
                    className="l-input"
                    style={{ flex: 1, minWidth: 0 }}
                    placeholder={keyStored ? "paste a new key to replace it" : "paste a key"}
                    value={keyInput}
                    onChange={(e) => setKeyInput(e.target.value)}
                  />
                  <button
                    className="l-btn"
                    disabled={busy || keyInput.trim() === ""}
                    onClick={() => {
                      const k = keyInput;
                      setKeyInput("");
                      void run(() => setAPIKey(provider.id, k), "Key stored in the keychain.", true);
                    }}
                  >
                    {keyStored ? "Replace key" : "Save key"}
                  </button>
                  {keyStored && (
                    <button className="l-btn l-btn-danger" disabled={busy} onClick={() => void run(() => clearAPIKey(provider.id), "Key removed from the keychain.", true)}>Remove key</button>
                  )}
                </div>
              )}
            </>
          )}

          <div style={row}>
            <span style={labelStyle}>model</span>
            <input
              aria-label="Model"
              className="l-input"
              list={modelListId}
              style={{ flex: 1, minWidth: 0, fontFamily: "var(--font-mono)" }}
              placeholder={provider.id === "anthropic" ? "claude-opus-5-5 (default)" : "choose a model"}
              value={draft.model}
              onChange={(e) => update({ model: e.target.value })}
            />
            <datalist id={modelListId}>
              {models.map((m) => <option key={m.id} value={m.id}>{m.displayName}</option>)}
            </datalist>
            <button
              className="l-btn"
              disabled={busy}
              onClick={async () => {
                const gen = draftGen.current;
                setBusy(true);
                const r = await listAIModels(draft);
                setBusy(false);
                if (gen !== draftGen.current) return; // the draft changed meanwhile
                setModels(r.models);
                setMessage(r.error ? { kind: "error", text: r.error } : { kind: "ok", text: `${r.models.length} model${r.models.length === 1 ? "" : "s"} available.` });
              }}
            >
              Load models
            </button>
          </div>

          <div style={row}>
            <span style={labelStyle}>{provider.id === "openaiCompatible" ? "server URL" : "custom origin"}</span>
            <input
              aria-label={provider.id === "openaiCompatible" ? "Server URL" : "Custom origin"}
              className="l-input"
              style={{ flex: 1, minWidth: 0, fontFamily: "var(--font-mono)" }}
              placeholder={provider.defaultOrigin ? `${provider.defaultOrigin} (default)` : "http://localhost:11434"}
              value={draft.baseUrl}
              onChange={(e) => update({ baseUrl: e.target.value })}
            />
          </div>
          {customOrigin && (
            <div data-testid="ai-origin-disclosure" style={{ ...hint, marginLeft: 118, color: "var(--color-text-warning)" }}>
              Your {providerLabel(provider.id).split(" (")[0]} key will be sent to {customOrigin}, not to {provider.defaultOrigin}.
            </div>
          )}

          {provider.id === "anthropic" && (
            <label style={{ ...row, cursor: "pointer" }}>
              <span style={labelStyle}>refusals</span>
              <input type="checkbox" checked={draft.refusalFallback} onChange={(e) => update({ refusalFallback: e.target.checked })} />
              <span style={{ fontSize: 11 }}>Let Anthropic retry a refused request on another Claude model (the result names the model used)</span>
            </label>
          )}
          {provider.id === "openaiCompatible" && (
            <label style={{ ...row, cursor: "pointer" }}>
              <span style={labelStyle}>TLS</span>
              <input type="checkbox" checked={draft.tlsSkipVerify} onChange={(e) => update({ tlsSkipVerify: e.target.checked })} />
              <span style={{ fontSize: 11 }}>Accept a self-signed certificate</span>
            </label>
          )}

          <label style={{ ...row, cursor: "pointer" }}>
            <span style={labelStyle}>protected clusters</span>
            <input
              type="checkbox"
              aria-label="Allow protected-cluster egress"
              checked={draft.allowProtectedEgress}
              onChange={(e) => {
                if (e.target.checked && !state.persisted.allowProtectedEgress) setConfirmEgress(true);
                else update({ allowProtectedEgress: e.target.checked });
              }}
            />
            <span style={{ fontSize: 11 }}>Send protected (prd) clusters' context to this provider. Off: Explain stays disabled there unless the model runs on this Mac.</span>
          </label>
        </>
      )}

      <div style={{ ...row, marginTop: 8 }}>
        <button className="l-btn l-btn-primary" disabled={busy} onClick={() => void save(draft)}>Save</button>
        {provider.id !== "off" && (
          <button
            className="l-btn"
            disabled={busy}
            onClick={async () => {
              const gen = draftGen.current;
              setBusy(true);
              const r = await testAIProvider(draft);
              setBusy(false);
              if (gen === draftGen.current) setTest(r); // else the draft changed meanwhile
            }}
          >
            Test
          </button>
        )}
        {busy && <span style={hint}>working…</span>}
      </div>
      {test && (
        <div data-testid="ai-test-result" style={{ fontSize: 11, color: test.ok ? "var(--color-text-primary)" : "var(--color-text-danger)" }}>
          {test.ok ? <>Test answered via {test.origin} · <span style={{ fontFamily: "var(--font-mono)" }}>{test.model}</span>: “{test.reply}”</> : <>Test failed: {test.error}</>}
        </div>
      )}
      {message && (
        <div data-testid="ai-message" role="status" style={{ fontSize: 11, marginTop: 4, color: message.kind === "error" ? "var(--color-text-danger)" : "var(--color-text-secondary)" }}>
          {message.kind === "error" ? "▲ " : ""}{message.text}
        </div>
      )}
      <div style={{ ...hint, marginTop: 6 }}>Test sends one short prompt with no cluster data. Saving writes the ai block of fleet.yaml; keys stay in the keychain.</div>

      {confirmEgress && (
        <ConfirmDialog
          title="Allow protected-cluster context to leave this Mac?"
          detail={`Explain and Suggest-fix on protected clusters will send their resolved state to ${providerLabel(draft.provider)}${draft.baseUrl ? ` at ${draft.baseUrl}` : ""}. This saves the settings now.`}
          cluster={AI_EGRESS_PHRASE}
          protected
          danger
          lockName="egress lock"
          showScope={false}
          confirmLabel="Allow and save"
          arm={armAIEgress}
          onCancel={() => setConfirmEgress(false)}
          onConfirm={() => {
            setConfirmEgress(false);
            const next = { ...draft, allowProtectedEgress: true };
            setDraft(next);
            void save(next);
          }}
        />
      )}
    </div>
  );
}

function Notice({ testId, children }: { testId: string; children: React.ReactNode }) {
  return (
    <div data-testid={testId} style={{ fontSize: 11, color: "var(--color-text-warning)", padding: "2px 0 6px", display: "flex", alignItems: "center", gap: 6 }}>
      <IconAlertTriangle size={11} stroke={1.7} style={{ flexShrink: 0 }} aria-hidden /> <span>{children}</span>
    </div>
  );
}
