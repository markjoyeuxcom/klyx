// MembershipNote: a fleet-wide read covers the clusters it pinned; when the
// fleet changes meanwhile, say which clusters the read does not cover.
export function MembershipNote({ joined, left }: { joined: string[]; left: string[] }) {
  if (joined.length === 0 && left.length === 0) return null;
  return (
    <span style={{ color: "var(--color-text-warning)", overflowWrap: "anywhere" }}>
      Fleet membership changed since this read{joined.length > 0 ? ` - joined: ${joined.join(", ")}` : ""}{left.length > 0 ? ` - left: ${left.join(", ")}` : ""}. Refresh to read the current fleet.
    </span>
  );
}
