# usage: python3 -I scripts/dev/mutate.py <checkout> <spec.json>
#
# A slice's mutation pass: each mutant is one exact edit (or several) to a
# file in the checkout, the named tests run against it, and the file is
# restored whatever happens. A mutant is CAUGHT when its tests fail (or
# hang past ten minutes), SURVIVED when they pass, BUILD when they could
# not build - rewrite it so it builds - and SKIP when its old text is not
# in the file exactly once. Never run it while anything else uses the
# checkout. The spec is a JSON list of:
#
#   {"name": ..., "file": <path in the checkout>, "old": ..., "new": ...,
#    "pkg": "./internal/fleet", "run": "^(TestA|TestB)$"}
#   {..., "edits": [{"old": ..., "new": ...}, ...]}    several edits at once
#   {..., "fe": "src/a.test.tsx src/b.test.tsx"}       vitest instead of go test
#   {..., "dir": "internal/metrics/promqlproof"}       go test from a nested module
import json, subprocess, sys, pathlib
root, spec = pathlib.Path(sys.argv[1]), json.load(open(sys.argv[2]))
caught = 0
for m in spec:
    f = root / m["file"]; orig = f.read_text()
    edits = m.get("edits") or [{"old": m["old"], "new": m["new"]}]
    text, bad = orig, None
    for e in edits:
        if text.count(e["old"]) != 1:
            bad = text.count(e["old"]); break
        text = text.replace(e["old"], e["new"])
    if bad is not None:
        print(f"SKIP(count={bad}) {m['name']}"); continue
    f.write_text(text)
    try:
        if m.get("fe"):
            cmd = ["npx", "vitest", "run", *m["fe"].split()]
            cwd = root / "cmd/klyx/frontend"
        else:
            cmd = ["go", "test", "-count=1", "-timeout", "120s", "-run", m["run"], m["pkg"]]
            cwd = root / m.get("dir", ".")
        try:
            r = subprocess.run(cmd, cwd=cwd, capture_output=True, text=True, timeout=600)
        except subprocess.TimeoutExpired:
            print(f"CAUGHT(hung) {m['name']}", flush=True); caught += 1
            continue
        out = r.stdout + r.stderr
        if r.returncode == 0:
            verdict = "SURVIVED"
        elif "[build failed]" in out or "setup failed" in out or ("FAIL" not in out and "Error" in out and m.get("fe") is None):
            verdict = "BUILD"
        else:
            verdict = "CAUGHT"; caught += 1
        print(f"{verdict} {m['name']}", flush=True)
    finally:
        f.write_text(orig)
print(f"{caught}/{len(spec)} caught")
