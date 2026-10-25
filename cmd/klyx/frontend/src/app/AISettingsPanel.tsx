import { useEffect, useId, useRef, useState } from "react";
import { IconAlertTriangle } from "@tabler/icons-react";
import {
  applyAI,
  applyAIQuiet,
  clearAPIKey,
  getAISettings,
  getCLIStatus,
  listAIModels,
  moveKeyToKeychain,
  planAI,
  planAIFromFile,
  planAIRevert,
  retryAI,
  setAPIKey,
  testAIProvider,
  type AICLIStatus,
  type AISettings,
  type AISettingsState,
  type AITest,
  type AIModel,
} from "../bridge/aiSettings";
import type { ApplyResult, Plan } from "../bridge/plan";
import { SettingField } from "../settings/SettingField";
import { PlanView } from "../settings/PlanView";
import { setSectionDraft } from "../settings/navGuard";

// AISettingsPanel is Settings > AI (spec 2026-10-13 §2.4). It edits a draft of
// fleet.yaml's ai block and shows exactly where prompts go. A key goes in
// through the keychain and never comes back to the page. A change is a plan
// from what runs (Settings slice F §5, §6): Review and apply shows what
// changes, what it ends and the interlock, and the backend applies it by
// its digest - written, then built and published - so what the status line
// says is what chat, Explain, Suggest-fix and investigations use.

export const PROVIDERS: { id: string; label: string; keyed: boolean; defaultOrigin?: string }[] = [
  { id: "off", label: "Off", keyed: false },
  { id: "anthropic", label: "Claude (Anthropic API key)", keyed: true, defaultOrigin: "https://api.anthropic.com" },
  { id: "openai", label: "OpenAI (API key)", keyed: true, defaultOrigin: "https://api.openai.com" },
  { id: "gemini", label: "Gemini (API key)", keyed: true, defaultOrigin: "https://generativelanguage.googleapis.com" },
  { id: "openaiCompatible", label: "Local server (OpenAI-compatible, e.g. Ollama)", keyed: true },
  { id: "claudeCLI", label: "Claude Code CLI (your Claude sign-in)", keyed: false },
];

export function providerLabel(id: string): string {
  return PROVIDERS.find((p) => p.id === id)?.label ?? id;
}

const row: React.CSSProperties = { display: "flex", alignItems: "center", gap: 8, padding: "4px 0" };
const hint: React.CSSProperties = { fontSize: 11, color: "var(--color-text-tertiary)" };

// differs reports an unsaved settings draft.
function differs(draft: AISettings, saved: AISettings): boolean {
  return (Object.keys(draft) as (keyof AISettings)[]).some((k) => (draft[k] ?? "") !== (saved[k] ?? ""));
}

// onState hands the read state to the section (its sentence is the In use
// line).
export function AISettingsPanel({ onState }: { onState?: (s: AISettingsState) => void } = {}) {
  const [state, setState] = useState<AISettingsState | null>(null);
  const [draft, setDraft] = useState<AISettings | null>(null);
  const [keyInput, setKeyInput] = useState("");
  const [models, setModels] = useState<AIModel[]>([]);
  const [message, setMessage] = useState<{ kind: "ok" | "error"; text: string } | null>(null);
  const [test, setTest] = useState<AITest | null>(null);
  const [busy, setBusy] = useState(false);
  // the plan under review: it belongs to the draft it was planned from, and
  // any edit voids it
  const [plan, setPlan] = useState<Plan | null>(null);
  const [loadFailed, setLoadFailed] = useState(false);
  const [cli, setCli] = useState<AICLIStatus | null>(null);
  const [cliCheck, setCliCheck] = useState(0); // bumped to re-read the CLI status
  const modelListId = useId();
  // Draft generation: every draft edit bumps it, and a model list or Test
  // reply for an older generation is dropped - a late reply for the previous
  // provider or origin must never land against the current draft (Codex
  // review of c488e34).
  const draftGen = useRef(0);
  // Only the latest read's reply lands, and it replaces the draft only when
  // nothing was edited while it was on its way (Codex's review of Settings
  // slice 0, finding 1).
  const loadSeq = useRef(0);
  // The guard's Save, set on each render that reaches the form; and the
  // save still being read back, which finishes before leaving is asked
  // about (Settings S1 §1).
  const saveRef = useRef<() => Promise<boolean>>(async () => false);
  const saving = useRef<Promise<unknown> | null>(null);

  // load reports whether the read succeeded (a newer read on its way counts:
  // it is the one that lands)
  const load = async (keepDraft = false): Promise<boolean> => {
    const seq = ++loadSeq.current;
    const gen = draftGen.current;
    const s = await getAISettings();
    if (seq !== loadSeq.current) return true; // a newer read is on its way
    setLoadFailed(s === null);
    if (!s) return false;
    setState(s);
    onState?.(s);
    if (!draft || (!keepDraft && gen === draftGen.current)) setDraft(s.persisted);
    return true;
  };
  useEffect(() => {
    void load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // Leaving the section with a draft asks first (S1 §1): the settings draft,
  // and a typed key as its own draft that Save never stores.
  useEffect(() => {
    const unsaved = !!state && !!draft && differs(draft, state.persisted);
    const typedKey = keyInput.trim() !== "";
    setSectionDraft("ai", unsaved || typedKey || saving.current ? {
      settings: unsaved,
      credentials: typedKey ? ["an unsaved API key"] : [],
      save: () => saveRef.current(),
      discard: () => {
        setKeyInput("");
        if (state) {
          draftGen.current++;
          setDraft(state.persisted);
        }
      },
      settling: () => saving.current,
    } : null);
  });
  useEffect(() => () => setSectionDraft("ai", null), []);

  // The installed Claude Code, described without running it. A reply for an
  // older path or provider is dropped.
  const cliPath = draft?.provider === "claudeCLI" ? draft.cliPath : null;
  useEffect(() => {
    if (cliPath === null || !draft) {
      setCli(null);
      return;
    }
    let live = true;
    setCli(null);
    void getCLIStatus(draft).then((st) => {
      if (live) setCli(st);
    });
    return () => {
      live = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [cliPath, cliCheck]);

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
  const run = async (fn: () => Promise<{ ok: boolean; error: string }>, okText: string, keepDraft = false): Promise<boolean> => {
    // an edit made while this runs is newer than its reply: the reload keeps
    // it (Settings review slice 0)
    const gen = draftGen.current;
    setBusy(true);
    const work = (async () => {
      const r = await fn();
      setBusy(false);
      setMessage(r.ok ? { kind: "ok", text: okText } : { kind: "error", text: r.error || "failed" });
      // saved and read back: a failed reread is not a verified save (R7)
      return r.ok && (await load(keepDraft || gen !== draftGen.current));
    })();
    saving.current = work;
    try {
      return await work;
    } finally {
      if (saving.current === work) saving.current = null;
    }
  };
  const update = (patch: Partial<AISettings>) => {
    draftGen.current++;
    setDraft({ ...draft, ...patch });
    setTest(null);
    setPlan(null);
    // A list belongs to one provider, origin and key: any change voids it.
    if ("provider" in patch || "baseUrl" in patch || "keySource" in patch || "apiKeyEnv" in patch) setModels([]);
    // and a typed key belongs to the provider and origin it was typed for:
    // it never moves to another (Settings review slice 0)
    if ("provider" in patch || "baseUrl" in patch || "keySource" in patch) setKeyInput("");
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
      cliPath: same ? state.persisted.cliPath : "",
    });
  };
  // applyWith runs an Apply and says its outcome: applied, and read back;
  // saved, not active; or refused with the plan that replaced it, shown
  // unconfirmed (keyed by its digest)
  const applyWith = async (call: () => Promise<ApplyResult>, okText: string): Promise<boolean> => {
    const gen = draftGen.current;
    setBusy(true);
    const work = (async () => {
      const r = await call();
      setBusy(false);
      if (r.ok) {
        setPlan(null);
        setMessage({ kind: "ok", text: okText });
        return await load(gen !== draftGen.current);
      }
      if (r.savedNotActive) {
        setPlan(null);
        setMessage({ kind: "error", text: `Saved, not active: ${r.savedNotActive}` });
        await load(true);
        return false;
      }
      if (r.plan) setPlan(r.plan);
      setMessage({ kind: "error", text: r.error || "Applying this ends running work or needs your confirmation: review it below." });
      return false;
    })();
    saving.current = work;
    try {
      return await work;
    } finally {
      if (saving.current === work) saving.current = null;
    }
  };
  const inUse = "Applied. Chat, Explain, Suggest-fix and investigations now use these settings.";
  const showPlan = async (get: () => Promise<Plan>) => {
    const gen = draftGen.current;
    setBusy(true);
    const p = await get();
    setBusy(false);
    if (gen === draftGen.current) setPlan(p); // else the draft changed meanwhile
  };
  // Save and leave (§6): applied only when it ends nothing and needs no
  // interlock; otherwise the plan is shown and the person stays
  saveRef.current = () => applyWith(() => applyAIQuiet(draft), inUse);

  const a = state.active;
  const customOrigin = provider.defaultOrigin && draft.baseUrl ? draft.baseUrl : "";
  // The status line is what is IN USE; a draft that differs from the saved
  // settings says so, so a chosen provider never reads as applied (UI review
  // 2026-10-13: "AI is off" sat beside a chosen, unsaved Claude Code).
  const dirty = differs(draft, state.persisted);
  // the saved settings are the ones in use unless they diverged (its notice)
  const unverified = !state.diverged && a.backend === "openaiCompatible" && state.persisted.tlsSkipVerify;

  return (
    <div data-testid="ai-settings">
      <div data-testid="ai-status" role="status" style={{ fontSize: 12, marginBottom: 8, color: a.problem ? "var(--color-text-warning)" : "var(--color-text-primary)" }}>
        <span className="t-secondary">{loadFailed ? "In use, as last read: " : "In use: "}</span>
        {a.backend === "off" || !a.backend
          ? "AI is off. Choose a provider to enable chat, Explain, Suggest-fix and investigations."
          : a.ready
            ? <>Prompts go to <span style={{ fontFamily: "var(--font-mono)" }}>{a.origin}</span> · {providerLabel(a.backend)} · <span style={{ fontFamily: "var(--font-mono)" }}>{a.model}</span>{unverified && <span data-testid="ai-tls-off" style={{ color: "var(--color-text-warning)" }}> · certificate not verified</span>}</>
            : <>AI is unavailable: {a.problem}</>}
      </div>
      {loadFailed && (
        // a reread after a change failed: what is in use is no longer known
        // (Settings review slice 0)
        <Notice testId="ai-reread-failed">
          Klyx could not read the AI settings again, so what is in use may have changed since.{" "}
          <button className="l-btn" disabled={busy} onClick={() => void load(true)}>Retry</button>
        </Notice>
      )}
      {dirty && (
        <div data-testid="ai-unsaved" role="status" style={{ fontSize: 11, marginBottom: 8, color: "var(--color-text-warning)" }}>
          Unsaved changes - Review and apply to use {providerLabel(draft.provider)}{draft.model ? ` · ${draft.model}` : ""}. Test checks the draft without saving it.
        </div>
      )}

      {state.savedNotActive && (
        <Notice testId="ai-saved-not-active">
          Saved, not active: {state.savedNotActive}. Nothing uses AI until this is retried or reverted.{" "}
          <button className="l-btn" disabled={busy} onClick={() => void applyWith(retryAI, inUse)}>Retry</button>
          <button className="l-btn" disabled={busy} onClick={() => void showPlan(planAIRevert)}>Revert</button>
        </Notice>
      )}
      {state.diverged && (
        <Notice testId="ai-diverged">
          The AI settings in fleet.yaml differ from the ones in use.{" "}
          <button className="l-btn" disabled={busy} onClick={() => void showPlan(planAIFromFile)}>Use the file's values</button>
        </Notice>
      )}
      {state.persistedError && <Notice testId="ai-persisted-error">fleet.yaml cannot be read: {state.persistedError}</Notice>}
      {state.keychainError && <Notice testId="ai-keychain-error">{state.keychainError}</Notice>}
      {state.legacyKeyInFile && (
        <Notice testId="ai-legacy-key">
          Your API key is stored in fleet.yaml in plain text.{" "}
          <button className="l-btn" disabled={busy} onClick={() => void run(moveKeyToKeychain, "Key moved to the keychain and removed from fleet.yaml.")}>Move to keychain</button>
        </Notice>
      )}

      <SettingField id="ai.provider" source="file">
        {(ids) => (
          <select id={ids.id} aria-describedby={ids.describedBy} className="l-input" value={draft.provider} onChange={(e) => chooseProvider(e.target.value)}>
            {PROVIDERS.map((p) => <option key={p.id} value={p.id}>{p.label}</option>)}
          </select>
        )}
      </SettingField>

      {provider.id !== "off" && (
        <>
          {provider.keyed && (
            <>
              <SettingField id="ai.keySource" source="file">
                {(ids) => (
                  <select id={ids.id} aria-describedby={ids.describedBy} className="l-input" value={draft.keySource === "legacy" ? "legacy" : draft.keySource} onChange={(e) => update({ keySource: e.target.value })}>
                    <option value="keychain">Keychain</option>
                    <option value="env">Environment variable</option>
                    {provider.id === "openaiCompatible" && <option value="none">No key</option>}
                    {draft.keySource === "legacy" && <option value="legacy">fleet.yaml (plain text)</option>}
                  </select>
                )}
              </SettingField>
              {draft.keySource === "env" && (
                <SettingField id="ai.apiKeyEnv" source="file">
                  {(ids) => <input id={ids.id} aria-describedby={ids.describedBy} className="l-input" placeholder="ANTHROPIC_API_KEY" value={draft.apiKeyEnv} onChange={(e) => update({ apiKeyEnv: e.target.value })} />}
                </SettingField>
              )}
              {draft.keySource === "keychain" && (
                <SettingField id="credential.ai-key" note={
                  <span data-testid="ai-key-state" style={{ color: keyStored ? "var(--color-text-primary)" : "var(--color-text-tertiary)" }}>
                    {keyStored ? "stored in the keychain" : "no key stored"}
                  </span>
                }>
                  {(ids) => (<>
                    <input
                      id={ids.id}
                      aria-describedby={ids.describedBy}
                      type="password"
                      autoComplete="off"
                      spellCheck={false}
                      className="l-input"
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
                  </>)}
                </SettingField>
              )}
            </>
          )}

          <SettingField id="ai.model" source={draft.model ? "file" : "default"}>
            {(ids) => (<>
              <input
                id={ids.id}
                aria-describedby={ids.describedBy}
                className="l-input"
                list={modelListId}
                style={{ fontFamily: "var(--font-mono)" }}
                placeholder={provider.id === "anthropic" || provider.id === "claudeCLI" ? "claude-opus-5-5 (default)" : "choose a model"}
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
            </>)}
          </SettingField>

          {provider.id === "claudeCLI" && (
            <SettingField id="ai.cliPath" source={draft.cliPath ? "file" : "default"} note={<>
              <div data-testid="ai-cli-status" style={{ color: cli?.problem ? "var(--color-text-warning)" : undefined }}>
                {cli === null ? (
                  "Checking the installed Claude Code…"
                ) : (
                  <>
                    {cli.path && <span style={{ fontFamily: "var(--font-mono)" }}>{cli.path}</span>}
                    {cli.tested && <> · Claude Code {cli.version} · sha256 {cli.sha256}… · {cli.admitted ? "admitted" : cli.problem ? "not admitted (Test admits it)" : "not admitted yet (Test or the first call admits it)"}</>}
                    {cli.problem && <div>{cli.problem}</div>}
                  </>
                )}
              </div>
              <div style={hint}>
                Klyx runs a Claude Code build (2.1.296 or later) only after Test has checked it on this machine, so each Claude Code update needs one Test. It runs signed in to a personal Pro or Max account, with no tools, MCP servers, hooks or settings. Before every call it re-checks the sign-in and administrator policy, and refuses if either changed.
              </div>
            </>}>
              {(ids) => (
                <input
                  id={ids.id}
                  aria-describedby={ids.describedBy}
                  className="l-input"
                  style={{ fontFamily: "var(--font-mono)" }}
                  placeholder="found automatically (PATH, then ~/.local/bin/claude)"
                  value={draft.cliPath}
                  onChange={(e) => update({ cliPath: e.target.value })}
                />
              )}
            </SettingField>
          )}

          {provider.id !== "claudeCLI" && (
            <SettingField id="ai.baseUrl" label={provider.id === "openaiCompatible" ? "Server URL" : "Custom origin"} source={draft.baseUrl ? "file" : "default"}
              note={customOrigin ? (
                <span data-testid="ai-origin-disclosure" style={{ color: "var(--color-text-warning)" }}>
                  Your {providerLabel(provider.id).split(" (")[0]} key will be sent to {customOrigin}, not to {provider.defaultOrigin}.
                </span>
              ) : undefined}>
              {(ids) => (
                <input
                  id={ids.id}
                  aria-describedby={ids.describedBy}
                  className="l-input"
                  style={{ fontFamily: "var(--font-mono)" }}
                  placeholder={provider.defaultOrigin ? `${provider.defaultOrigin} (default)` : "http://localhost:11434"}
                  value={draft.baseUrl}
                  onChange={(e) => update({ baseUrl: e.target.value })}
                />
              )}
            </SettingField>
          )}

          {provider.id === "anthropic" && (
            <SettingField id="ai.refusalFallback" source="file">
              {(ids) => <input id={ids.id} aria-describedby={ids.describedBy} type="checkbox" checked={draft.refusalFallback} onChange={(e) => update({ refusalFallback: e.target.checked })} />}
            </SettingField>
          )}
          {provider.id === "openaiCompatible" && (
            <SettingField id="ai.tlsSkipVerify" source="file">
              {(ids) => <input id={ids.id} aria-describedby={ids.describedBy} type="checkbox" checked={draft.tlsSkipVerify} onChange={(e) => update({ tlsSkipVerify: e.target.checked })} />}
            </SettingField>
          )}

          <SettingField id="ai.allowProtectedEgress" source="file">
            {(ids) => (
              <input
                id={ids.id}
                aria-describedby={ids.describedBy}
                type="checkbox"
                checked={draft.allowProtectedEgress}
                // turning it on is the plan's interlock (§5), typed at Apply
                onChange={(e) => update({ allowProtectedEgress: e.target.checked })}
              />
            )}
          </SettingField>

          {/* AI A3 §6: on by default; off removes the tool, the snapshot
              instruction and the card - the remedy stays text */}
          <SettingField id="ai.proposeActions" source={draft.proposeActions === undefined ? "default" : "file"}>
            {(ids) => <input id={ids.id} aria-describedby={ids.describedBy} type="checkbox" checked={draft.proposeActions ?? true} onChange={(e) => update({ proposeActions: e.target.checked })} />}
          </SettingField>
        </>
      )}

      <div style={{ ...row, marginTop: 8 }}>
        <button className="l-btn l-btn-primary" disabled={busy} onClick={() => void showPlan(() => planAI(draft))}>Review and apply</button>
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
              if (provider.id === "claudeCLI") setCliCheck((n) => n + 1); // Test admits, or revokes
            }}
          >
            Test
          </button>
        )}
        {busy && <span style={hint}>working…</span>}
      </div>
      {plan && (
        <PlanView
          key={plan.digest || plan.refused}
          plan={plan}
          busy={busy}
          interlockTitle="Allow protected-cluster context to leave this machine?"
          onApply={async (typed) => {
            let result: ApplyResult = { ok: false };
            await applyWith(async () => (result = await applyAI(plan.digest, typed)), inUse);
            return result;
          }}
          onCancel={() => setPlan(null)}
        />
      )}
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
      <div style={{ ...hint, marginTop: 6 }}>
        {provider.id === "claudeCLI"
          ? "Test re-checks the Claude Code build, sign-in and policy, then sends one short prompt with no cluster data on your Claude sign-in. The prompt asks Claude Code to read a file and run a command; an answer that shows it could fails the Test and revokes the CLI. A passing Test admits this Claude Code build until it changes."
          : "Test sends one short prompt with no cluster data."}{" "}
        Applying writes the ai block of fleet.yaml; keys stay in the keychain.
      </div>

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
