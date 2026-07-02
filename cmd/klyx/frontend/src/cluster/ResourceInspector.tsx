import { useState } from "react";

export const inspectorStateBox: React.CSSProperties = {
  color: "var(--color-text-secondary)",
  fontSize: 13,
  padding: 18,
  border: "0.5px solid var(--color-border-tertiary)",
  borderRadius: "var(--border-radius-md)",
  background: "var(--color-background-primary)",
};

export function InspectorSection({
  title,
  children,
  count,
  collapsible = false,
  defaultCollapsed = false,
}: {
  title: string;
  children: React.ReactNode;
  count?: number;
  collapsible?: boolean;
  defaultCollapsed?: boolean;
}) {
  const [collapsed, setCollapsed] = useState(defaultCollapsed);
  const label = count === undefined ? title : `${title} (${count})`;

  if (!collapsible) {
    return (
      <div style={sectionShell}>
        <div style={sectionTitle}>{label}</div>
        <div style={sectionBody}>{children}</div>
      </div>
    );
  }

  return (
    <div style={sectionShell}>
      <button
        type="button"
        onClick={() => setCollapsed((v) => !v)}
        aria-expanded={!collapsed}
        style={sectionToggle}
      >
        <span style={{ width: 12, color: "var(--color-text-tertiary)" }}>{collapsed ? ">" : "v"}</span>
        <span style={sectionTitle}>{label}</span>
      </button>
      {!collapsed && <div style={sectionBody}>{children}</div>}
    </div>
  );
}

export function InspectorTabButton({
  active,
  children,
  onClick,
}: {
  active: boolean;
  children: React.ReactNode;
  onClick: () => void;
}) {
  return (
    <button type="button" onClick={onClick} style={tabButton(active)}>
      {children}
    </button>
  );
}

export function InspectorTitle({ title, sub }: { title: string; sub: string }) {
  return (
    <div>
      <div style={titleStyle} title={title}>{title}</div>
      <div style={subTitleStyle} title={sub}>{sub}</div>
    </div>
  );
}

export function InspectorEmpty({ text, style }: { text: string; style?: React.CSSProperties }) {
  return <div style={{ ...inspectorStateBox, ...style }}>{text}</div>;
}

export function MetadataChips({
  title,
  values,
  defaultCollapsed = false,
}: {
  title: string;
  values: Record<string, string> | undefined;
  defaultCollapsed?: boolean;
}) {
  const entries = Object.entries(values ?? {}).sort(([a], [b]) => a.localeCompare(b));
  if (entries.length === 0) return null;
  return (
    <InspectorSection title={title} count={entries.length} collapsible defaultCollapsed={defaultCollapsed}>
      <div style={{ display: "flex", flexWrap: "wrap", gap: 6 }}>
        {entries.map(([k, v]) => (
          <span key={k} title={`${k}=${v}`} style={metadataChip}>
            {k}={v}
          </span>
        ))}
      </div>
    </InspectorSection>
  );
}

const sectionShell: React.CSSProperties = { marginBottom: 12 };

const sectionTitle: React.CSSProperties = {
  fontSize: 10,
  textTransform: "uppercase",
  letterSpacing: 0.5,
  color: "var(--color-text-tertiary)",
};

const sectionBody: React.CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 3,
  fontSize: 12,
};

const sectionToggle: React.CSSProperties = {
  width: "100%",
  display: "flex",
  alignItems: "center",
  gap: 2,
  padding: "0 0 4px",
  border: 0,
  background: "transparent",
  color: "var(--color-text-tertiary)",
  cursor: "pointer",
  textAlign: "left",
};

const metadataChip: React.CSSProperties = {
  maxWidth: "100%",
  overflow: "hidden",
  textOverflow: "ellipsis",
  whiteSpace: "nowrap",
  background: "var(--color-background-secondary)",
  color: "var(--color-text-secondary)",
  fontSize: 10,
  padding: "1px 6px",
  borderRadius: 3,
  fontFamily: "var(--font-mono)",
};

const titleStyle: React.CSSProperties = {
  overflow: "hidden",
  textOverflow: "ellipsis",
  whiteSpace: "nowrap",
  fontFamily: "var(--font-mono)",
  fontSize: 14,
  fontWeight: 600,
};

const subTitleStyle: React.CSSProperties = {
  overflow: "hidden",
  textOverflow: "ellipsis",
  whiteSpace: "nowrap",
  color: "var(--color-text-tertiary)",
  fontSize: 11,
  marginTop: 3,
};

const baseButton: React.CSSProperties = {
  padding: "3px 10px",
  fontSize: 11,
  borderRadius: 4,
  cursor: "pointer",
  border: "0.5px solid var(--color-border-tertiary)",
  background: "var(--color-background-primary)",
  color: "var(--color-text-primary)",
};

function tabButton(active: boolean): React.CSSProperties {
  return {
    ...baseButton,
    color: active ? "var(--color-text-info)" : "var(--color-text-secondary)",
    background: active ? "var(--color-background-info)" : "var(--color-background-primary)",
    border: active ? "0.5px solid var(--color-border-info)" : "0.5px solid var(--color-border-tertiary)",
  };
}
