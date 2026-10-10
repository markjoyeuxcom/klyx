// fitColumns chooses which table columns fit a measured body width. The flex
// (identity) column keeps its floor; the others are added in priority order
// (lower number = more important) while their floors still fit. One column
// definition then drives both the header and the rows, so they cannot drift.
// Spec: "collapse optional columns before name identity".

export type ColumnDef = {
  id: string;
  /** floor width in px; the flex column grows beyond it */
  width: number;
  /** lower = kept longer; the flex column is always kept */
  priority: number;
  flex?: boolean;
  /** optional fr weight: a visible column with grow shares spare width with
   * the flex column instead of sitting at its floor */
  grow?: number;
};

export const ROW_PADDING = 16; // 8px each side
export const COLUMN_GAP = 12;

export function fitColumns(cols: readonly ColumnDef[], available: number): { visible: Set<string>; template: string; minWidth: number } {
  const keep = new Set(cols.filter((c) => c.flex).map((c) => c.id));
  const total = (ids: Set<string>) => {
    const chosen = cols.filter((c) => ids.has(c.id));
    return chosen.reduce((s, c) => s + c.width, 0) + Math.max(0, chosen.length - 1) * COLUMN_GAP + ROW_PADDING;
  };
  for (const c of [...cols].filter((c) => !c.flex).sort((a, b) => a.priority - b.priority)) {
    const trial = new Set(keep);
    trial.add(c.id);
    if (total(trial) <= available) keep.add(c.id);
  }
  const template = cols.filter((c) => keep.has(c.id))
    .map((c) => (c.flex ? `minmax(${c.width}px, 1fr)` : c.grow ? `minmax(${c.width}px, ${c.grow}fr)` : `${c.width}px`))
    .join(" ");
  return { visible: keep, template, minWidth: total(keep) };
}
