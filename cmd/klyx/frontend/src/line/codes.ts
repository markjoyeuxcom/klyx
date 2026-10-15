// clusterCodes derives a short identifying code per cluster (The Line slice
// 8a: the retired spine's spineCodes, restored unchanged as the one source a
// LinePos pairs with). Codes are unique and stable for a given fleet:
//   1. first+last segment initials ("homelab-blue" -> "hb", "prd-weu" -> "pw");
//   2. within a group of names that collide, the initials of the segments
//      they do not share ("aks-dev-weu-platform-01" -> "dw");
//   3. still colliding: two-letter prefixes of those segments ("homelab-blue"
//      vs "homelab-bee" -> "bl" / "be", "aks-dev-weu-…" vs "aks-dev-wus-…" ->
//      "we" / "wu");
//   4. anything left gets a numbered suffix in fleet order ("ak1", "ak2").
export function clusterCodes(names: string[]): Record<string, string> {
  const segsOf = (n: string) => n.split(/[-_.:/]+/).filter(Boolean);
  const initials = (n: string) => {
    const segs = segsOf(n);
    if (segs.length <= 1) return (segs[0] ?? n).slice(0, 2);
    return segs[0][0] + segs[segs.length - 1][0];
  };
  const codes: Record<string, string> = {};
  for (const n of names) codes[n] = initials(n);

  const collisions = (): string[][] => {
    const by = new Map<string, string[]>();
    for (const n of names) by.set(codes[n], [...(by.get(codes[n]) ?? []), n]);
    return [...by.values()].filter((g) => g.length > 1);
  };
  const distinguish = (shorten: (rest: string[]) => string) => {
    for (const group of collisions()) {
      const split = group.map(segsOf);
      const common = split.reduce((acc, segs) => acc.filter((x) => segs.includes(x)), split[0]);
      group.forEach((n, i) => {
        const rest = split[i].filter((x) => !common.includes(x));
        if (rest.length > 0) codes[n] = shorten(rest);
      });
    }
  };
  distinguish((rest) => rest.slice(0, 3).map((x) => x[0]).join(""));
  distinguish((rest) => rest.map((x) => x.slice(0, 2)).join("").slice(0, 4));
  for (const group of collisions()) {
    const taken = new Set(Object.values(codes));
    group.forEach((n, i) => {
      if (i === 0) return;
      let k = i + 1;
      while (taken.has(`${codes[n].slice(0, 3)}${k}`)) k++;
      codes[n] = `${codes[n].slice(0, 3)}${k}`;
      taken.add(codes[n]);
    });
  }
  return codes;
}
