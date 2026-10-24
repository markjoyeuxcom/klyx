"""The eval self-test's expectations (eval design §9): each scripted case's
report must give the status and the marks the design defines.

usage: python3 -I selftest.py <selftest output root>
"""
import json
import os
import sys


def run(root, case, scenario):
    path = os.path.join(root, case, "report.json")
    if not os.path.exists(path):
        return None
    with open(path) as f:
        rep = json.load(f)
    if oct(os.stat(path).st_mode & 0o777) != "0o600":
        raise AssertionError(f"{path} is not private")
    for r in rep["runs"]:
        if r["scenario"] == scenario:
            return r
    return None


def evidence(r, pred):
    for o in (r.get("record") or {}).get("predicates") or []:
        if o["predicate"] == pred:
            return o
    return {}


def marks(r):
    return r.get("marks") or {}


E1, E2, E3, E4 = "e1-flux-crashloop", "e2-healthy-control", "e3-keda-no-target", "e4-lb-pool-exhausted"

CASES = [
    ("grounded", E1, "completed", lambda r: marks(r).get("groundedCause") and evidence(r, "e1-diff").get("established") and evidence(r, "e1-log").get("established")),
    ("grounded", E2, "completed", lambda r: marks(r).get("control") is True and marks(r).get("explicitEmptyReport")),
    ("negated", E1, "completed", lambda r: marks(r).get("groundedCause") and marks(r).get("proxyCause") == 1),
    ("at-once", E1, "completed", lambda r: not marks(r).get("groundedCause") and evidence(r, "e1-diff").get("reason") == "not-requested" and evidence(r, "e1-log").get("reason") == "not-requested"),
    ("current-log", E1, "completed", lambda r: not marks(r).get("groundedCause") and evidence(r, "e1-log").get("reason") == "no-match"),
    ("no-report", E1, "report-failure", lambda r: r.get("marks") is None),
    ("empty-then-fail", E2, "model-failure", lambda r: r.get("marks") is None),
    ("unsupported", E2, "completed", lambda r: marks(r).get("control") is False and marks(r).get("unsupportedCauses") == 1),
    ("no-cause-proven", E2, "completed", lambda r: marks(r).get("control") is True and marks(r).get("proxyCause") == 0),
    ("snapshot-block", E1, "completed", lambda r: r.get("mode") == "snapshot" and r.get("fallbackToSnapshot") and evidence(r, "e1-diff").get("established")),
    ("snapshot-none", E1, "report-failure", lambda r: r["record"]["report"] == "missing"),
    ("snapshot-malformed", E1, "report-failure", lambda r: r["record"]["report"] == "malformed"),
    ("grounded-e3", E3, "completed", lambda r: marks(r).get("groundedCause") and evidence(r, "e3-event").get("established") and evidence(r, "e3-condition").get("established") and not marks(r).get("citedLookalike")),
    ("grounded-e4", E4, "completed", lambda r: marks(r).get("groundedCause") and evidence(r, "e4-event").get("established") and not marks(r).get("citedLookalike")),
    ("lookalike-e3", E3, "completed", lambda r: marks(r).get("citedLookalike") and not marks(r).get("groundedCause")),
    ("lookalike-e4", E4, "completed", lambda r: marks(r).get("citedLookalike") and not marks(r).get("groundedCause")),
    ("broken-plant", "s1-broken-plant", "not-ready", lambda r: "spec.replicas" in r.get("detail", "")),
    ("no-observer-part", "s2-no-observer-part", "sut-not-ready", lambda r: "no part for configmaps" in r.get("detail", "")),
    ("drift", "s3-drift", "drift", lambda r: "data.value" in r.get("detail", "")),
]


def main():
    root = sys.argv[1]
    failed = 0
    for case, scenario, status, check in CASES:
        r = run(root, case, scenario)
        if r is None:
            print(f"FAIL {case:20} {scenario:22} no report")
            failed += 1
            continue
        ok = r["status"] == status and bool(check(r))
        print(f"{'ok  ' if ok else 'FAIL'} {case:20} {scenario:22} {r['status']:15} {r.get('detail', '')[:90]}")
        if not ok:
            failed += 1
            print("     " + json.dumps({k: r.get(k) for k in ("status", "detail", "mode", "marks", "record")})[:1500])
    print(f"{len(CASES) - failed}/{len(CASES)} cases as the design defines")
    sys.exit(1 if failed else 0)


main()
