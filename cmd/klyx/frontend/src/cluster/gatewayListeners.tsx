import type { ListenerConditionDTO, ListenerDTO, ListenerSetRowDTO, NamespaceAllowanceDTO, RouteKindDTO } from "../store/fleet";

// gatewayListeners - a listener with its controller's status (coverage S7a
// §3), shared by the Gateway and ListenerSet inspectors. Klyx's verdict is
// the Go rule's (gwapi.ListenerIssues); this only presents the evidence: the
// conditions a controller reported, failing ones first, and the ones that
// describe an older generation marked as shown, never judged.

// The polarity a healthy listener reports; any other current value of these
// is the controller saying it is failing.
const HEALTHY: Record<string, string> = { Accepted: "True", Conflicted: "False", ResolvedRefs: "True", Programmed: "True" };
const ORDER = ["Accepted", "Conflicted", "ResolvedRefs", "Programmed"];

export const failing = (c: ListenerConditionDTO) => !c.stale && c.type in HEALTHY && (c.status === "True" || c.status === "False") && c.status !== HEALTHY[c.type];

export function listenerIssues(l: ListenerDTO): ListenerConditionDTO[] {
  return (l.status?.conditions ?? []).filter(failing).sort((a, b) => ORDER.indexOf(a.type) - ORDER.indexOf(b.type));
}

// issueGroups joins failing conditions that carry the same reason and
// message: Envoy Gateway writes one conflict's message on Accepted,
// Conflicted and Programmed alike, and it is said once.
export function issueGroups(issues: ListenerConditionDTO[]): { heads: string; why: string }[] {
  const out: { heads: string[]; why: string }[] = [];
  for (const c of issues) {
    const why = [c.reason, c.message].filter(Boolean).join(": ");
    const g = out.find((x) => x.why === why);
    if (g) g.heads.push(`${c.type} ${c.status}`);
    else out.push({ heads: [`${c.type} ${c.status}`], why });
  }
  return out.map((g) => ({ heads: g.heads.join(", "), why: g.why }));
}

const kindText = (k: RouteKindDTO) => k.kind;

export function endpointText(l: ListenerDTO): string {
  const tls = l.tlsMode ? ` · TLS ${l.tlsMode}` : (l.certificateRefs?.length ?? 0) > 0 ? " · TLS" : "";
  return `${l.protocol}${l.port > 0 ? `:${l.port}` : ""}${l.hostname ? ` · ${l.hostname}` : ""}${tls}`;
}

// allowedRoutesText says which routes the listener admits, as written: the
// API's defaults (same namespace, the protocol's kinds) when unset.
export function allowedRoutesText(l: ListenerDTO): string {
  const ns = l.allowedRoutes?.namespaces;
  const from = !ns || !ns.from ? "Same (the default)" : ns.from === "Selector" ? `Selector${ns.selector ? ` ${ns.selector}` : ns.selectorInvalid ? " (unreadable)" : ""}` : ns.from;
  const kinds = (l.allowedRoutes?.kinds ?? []).map(kindText);
  return `routes from ${from}${kinds.length > 0 ? ` · kinds ${kinds.join(", ")}` : ""}`;
}

export function ListenerList({ listeners, testid }: { listeners: ListenerDTO[]; testid?: string }) {
  if (listeners.length === 0) return <div className="t-secondary">No listeners.</div>;
  return (
    <div data-testid={testid ?? "listener-list"} style={{ display: "grid", gap: 10 }}>
      {listeners.map((l, i) => <ListenerItem key={`${l.name}|${i}`} l={l} />)}
    </div>
  );
}

function ListenerItem({ l }: { l: ListenerDTO }) {
  const st = l.status;
  const conds = st?.conditions ?? [];
  const bad = listenerIssues(l);
  const current = conds.filter((c) => !c.stale && !bad.includes(c));
  const older = conds.filter((c) => c.stale);
  return (
    <div data-testid={`listener-${l.name}`} style={{ display: "grid", gap: 2, minWidth: 0 }}>
      <div className="t-body" style={{ overflowWrap: "anywhere" }}>
        <span style={{ color: "var(--color-text-primary)", fontWeight: 500 }}>{l.name || "unnamed"}</span>
        <span style={{ color: "var(--color-text-secondary)" }}> {endpointText(l)}</span>
      </div>
      <div className="t-secondary" style={{ overflowWrap: "anywhere" }}>{allowedRoutesText(l)}</div>
      {!st ? <div className="t-secondary">No status reported for this listener.</div> : (
        <>
          <div className="t-secondary">
            {st.attachedRoutes} route{st.attachedRoutes === 1 ? "" : "s"} attached{st.supportedKinds.length > 0 ? ` · supports ${st.supportedKinds.map(kindText).join(", ")}` : ""}
          </div>
          {issueGroups(bad).map((g) => (
            <div key={g.heads} data-testid="listener-issue" className="t-body" style={{ color: "var(--color-text-warning)", overflowWrap: "anywhere" }}>
              {g.heads}{g.why ? ` - ${g.why}` : ""}
            </div>
          ))}
          {current.length > 0 && <div className="t-secondary" style={{ overflowWrap: "anywhere" }}>{current.map((c) => `${c.type} ${c.status}${c.status === "Unknown" && c.reason ? ` (${c.reason})` : ""}`).join(" · ")}</div>}
          {older.length > 0 && (
            <div data-testid="listener-older" className="t-secondary" style={{ color: "var(--color-text-tertiary)", overflowWrap: "anywhere" }}>
              {older.map((c) => `${c.type} ${c.status}`).join(" · ")} - observed at generation {Math.max(...older.map((c) => c.observedGeneration))}, shown, not judged
            </div>
          )}
        </>
      )}
    </div>
  );
}

// ---- ListenerSets ----------------------------------------------------------

export const PARENT_TEXT: Record<string, string> = {
  found: "",
  "not found": "not found - absent from a complete, current read of the Gateways",
  "not established": "not established - the Gateways read is incomplete, not current or not served",
  unsupported: "not a Gateway - Klyx reads Gateway parents only",
};

/** parentText: the parent as established; one found only in a stale read
 * is said to be last read, never current. */
export const parentText = (s: Pick<ListenerSetRowDTO, "parentState" | "parentCurrent">) =>
  s.parentState === "found" && s.parentCurrent === false ? "found in the last good read of the Gateways - not a current read" : PARENT_TEXT[s.parentState] ?? s.parentState;

export const parentKey = (s: Pick<ListenerSetRowDTO, "parent">) => `${s.parent.namespace}/${s.parent.name}`;

// allowedListenersText is the Gateway's allowedListeners as written.
export function allowedListenersText(a: NamespaceAllowanceDTO | undefined): string {
  if (!a || !a.set) return "allowedListeners not set - no ListenerSets are allowed";
  const from = a.from || "None";
  if (from === "Selector") return `ListenerSets from namespaces matching ${a.selector || (a.selectorInvalid ? "an unreadable selector" : "no selector")}`;
  return { None: "no ListenerSets allowed (None)", Same: "ListenerSets from the Gateway's own namespace (Same)", All: "ListenerSets from every namespace (All)" }[from] ?? `allowedListeners from ${from}`;
}

export const ordinal = (n: number) => `${n}${n % 100 >= 11 && n % 100 <= 13 ? "th" : ["th", "st", "nd", "rd"][n % 10] ?? "th"}`;
