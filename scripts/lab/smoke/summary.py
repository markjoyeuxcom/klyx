# usage: python3 -I summary.py <walk.json>
# One line per walk: the file, its verdict, the commit it ran, clean or
# dirty, steps passed of all, and the names of the steps that failed.
import json, os, sys
p = sys.argv[1]
try:
    d = json.load(open(p))
except Exception as e:
    print(os.path.basename(p), "unreadable", type(e).__name__)
    sys.exit(0)
pv = d.get("provenance") or {}
steps = d.get("steps") or []
ok = sum(1 for s in steps if s.get("ok"))
print(os.path.basename(p), d.get("result"), (pv.get("commit") or "?")[:7], "dirty" if pv.get("dirty") else "clean", f"{ok}/{len(steps)}", [s["name"] for s in steps if not s.get("ok")])
