#!/bin/zsh
# walk.sh - the native smoke (The Line slice 12b): builds the smoke bundle
# from this checkout and walks it against the labs (README.md):
#
#   scripts/lab/smoke/walk.sh          four ways: night and day, at 1440x900 and 1024x768
#   scripts/lab/smoke/walk.sh --one    night at 1440x900 alone
#
# Run labs.sh apply within the hour before: some assertions age (S4's grace,
# S2a's recent backup, the certificates). The bundle, its fleet and each
# walk's JSON and log are in the state directory; the app reads the state
# directory's kubeconfig alone (KUBECONFIG), so it sees kind-klyx and none
# of the person's own contexts. The walks edit the smoke fleet and undo
# each edit: the fleet is copied fresh before every walk and compared
# after, so a walk that failed part-way never hands the next a changed one.
# The AI steps read the scripted model (eval/fake-model.mjs, a3-plan) on
# 127.0.0.1:18792, started here unless one already answers there. The
# bindings come from $WAILS3 (default: wails3 on PATH), the version go.mod
# pins.
source ${0:A:h}/lib.sh && target

ways=(night-1440 night-1024 day-1440 day-1024)
[[ ${1:-} == --one ]] && ways=(night-1440)
repo=${SMOKE_ROOT:h:h:h}
app=$dir/KlyxSmoke.app
cfg=$dir/cfg
out=$dir/walks/$(date -u +%Y%m%dT%H%M%SZ)
mkdir -p $app/Contents/MacOS $cfg $out
cp $SMOKE_ROOT/Info.plist $app/Contents/Info.plist

# the bindings are generated per checkout (gitignored), so a stale copy
# would bundle a new service call as undefined
wails3=${WAILS3:-wails3}
command -v $wails3 > /dev/null || die "no wails3: set WAILS3, or install the version go.mod pins (README.md)"
(cd $repo/cmd/klyx && t 600 $wails3 generate bindings -clean=true -ts > $out/bindings.log 2>&1) || die "bindings failed (see $out/bindings.log)"
(cd $repo/cmd/klyx/frontend && t 600 npx tsc --noEmit > $out/tsc.log 2>&1 && t 600 npx vite build > $out/vite.log 2>&1) || die "frontend build failed (see $out)"
(cd $repo/cmd/klyx && t 900 go build -tags smoke -o $app/Contents/MacOS/klyx . > $out/build.log 2>&1) || die "go build failed (see $out/build.log)"
codesign --force --deep --sign - $app > /dev/null 2>&1 || die "codesign failed"

model=
if ! nc -z 127.0.0.1 18792 2>/dev/null; then
	node $repo/scripts/lab/eval/fake-model.mjs 18792 > $out/fake-model.log 2>&1 &
	model=$!
	trap '[[ -n $model ]] && kill $model 2>/dev/null' EXIT
	for i in {1..50}; do nc -z 127.0.0.1 18792 2>/dev/null && break; sleep 0.1; done
fi

for w in $ways; do
	theme=${w%-*} width=${w#*-} height=900
	[[ $width == 1024 ]] && height=768
	cp $SMOKE_ROOT/fleet.yaml $cfg/fleet.yaml && chmod 600 $cfg/fleet.yaml
	: > $out/$w.json
	KUBECONFIG=$kubeconfig KLYX_CONFIG=$cfg/fleet.yaml KLYX_SMOKE_OUT=$out/$w.json KLYX_SMOKE_THEME=$theme \
		KLYX_SMOKE_WIDTH=$width KLYX_SMOKE_HEIGHT=$height KLYX_SMOKE_CLUSTER=$ctx \
		t 960 $app/Contents/MacOS/klyx > $out/$w.log 2>&1
	python3 -I $SMOKE_ROOT/summary.py $out/$w.json
	cmp -s $SMOKE_ROOT/fleet.yaml $cfg/fleet.yaml && print "  fleet.yaml as it began" || print "  fleet.yaml CHANGED by the walk"
done
print -r -- "walks: $out"
