#!/usr/bin/env bash
# mutate_atm1123_hardening.sh — paired mutation proof for the ATM-1123 bounded,
# rate-limit-friendly ClickUp push (throttle.go, ApplyWith, title-drift).
# Purpose:   each mutation breaks ONE load-bearing behaviour in a scratch COPY of
#            the module and the matching unit test MUST fail (a surviving
#            mutation = a test that proves nothing). The real tree is never edited.
# Usage:     bash scripts/mutate_atm1123_hardening.sh
# Output:    one line per mutation (KILLED / SURVIVED), summary, exit 1 if any survive.
set -uo pipefail
SRC="$(cd "$(dirname "$0")/.." && pwd)"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/tb_mut.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT
fail=0; total=0
run_mut() { # name file sed-expr test-regex pkg
	local name="$1" file="$2" expr="$3" re="$4" pkg="$5"
	rm -rf "$WORK/m"; mkdir -p "$WORK/m"
	( cd "$SRC" && tar --exclude=./bin --exclude=./.git -cf - . ) | ( cd "$WORK/m" && tar -xf - )
	cp "$WORK/m/$file" "$WORK/orig.go"
	sed -i -E "$expr" "$WORK/m/$file"
	if cmp -s "$WORK/orig.go" "$WORK/m/$file"; then
		echo "MUTATION-NOT-APPLIED $name (sed changed nothing in $file)"; fail=1; total=$((total+1)); return
	fi
	if ! ( cd "$WORK/m" && go build ./... >/dev/null 2>&1 ); then
		echo "BROKEN-MUTANT $name (does not compile — a compile error is not a kill)"; fail=1; total=$((total+1)); return
	fi
	total=$((total+1))
	if ( cd "$WORK/m" && go test "$pkg" -run "$re" -count=1 >/dev/null 2>&1 ); then
		echo "SURVIVED $name"; fail=1
	else
		echo "KILLED   $name"
	fi
}
# control: unmutated copy must pass
rm -rf "$WORK/c"; mkdir -p "$WORK/c"
( cd "$SRC" && tar --exclude=./bin --exclude=./.git -cf - . ) | ( cd "$WORK/c" && tar -xf - )
if ( cd "$WORK/c" && go test ./pkg/client/ ./pkg/syncengine/ -count=1 >/dev/null 2>&1 ); then echo "CONTROL  unmutated copy passes"; else echo "CONTROL-FAIL unmutated copy fails"; exit 2; fi

run_mut M1-no-429-sleep      pkg/client/throttle.go   's|t\.cfg\.Sleep\(d\)|_ = d|'                                         'TestCreateTaskIsRealPostWith429Retry' ./pkg/client/
run_mut M2-no-retry-cap      pkg/client/throttle.go   's|if d > maxRetryAfter|if false \&\& d > maxRetryAfter|'            'TestRetryAfterBeyondCapStopsInsteadOfStalling' ./pkg/client/
run_mut M3-unbounded-retries pkg/client/throttle.go   's|attempt >= t\.cfg\.Max429Retries \{|attempt >= t.cfg.Max429Retries+50 {|' 'TestRetriesAreBounded' ./pkg/client/
run_mut M4-no-pacing         pkg/client/throttle.go   's|t\.cfg\.MinInterval > 0 \&\&|false \&\&|'                            'TestMinIntervalPacesRequests' ./pkg/client/
run_mut M5-retry-no-body     pkg/client/throttle.go   's|r\.Body = body|_ = body|'                              'TestCreateTaskIsRealPostWith429Retry' ./pkg/client/
run_mut M6-update-overwrites pkg/client/clickup.go    's|^(\t+)Description: t\.Description,|\1Description: "overwrite",|' 'TestUpdateTaskNameOnlyBodyOmitsOtherFields' ./pkg/client/
run_mut M7-limit-ignored     pkg/syncengine/reconcile.go 's|if opts\.Limit > 0 \&\& res\.Attempted >= opts\.Limit|if false|' 'TestApplyWithLimitAndNewestFirst' ./pkg/syncengine/
run_mut M8-stop-ignored      pkg/syncengine/reconcile.go 's|len\(errs\) > 0 \&\& opts\.StopOnError|false|'                  'TestApplyWithStopOnError' ./pkg/syncengine/
run_mut M9-oldest-first      pkg/syncengine/reconcile.go 's|return ni > nj|return ni < nj|'                                   'TestApplyWithLimitAndNewestFirst' ./pkg/syncengine/
run_mut M10-no-title-drift   pkg/syncengine/reconcile.go 's|e\.TitleDrift = strings\.TrimSpace\(t\.Name\) != strings\.TrimSpace\(wantTitle\)|e.TitleDrift = false|' 'TestTitleOnlyDriftGetsNameOnlyUpdate' ./pkg/syncengine/
run_mut M11-title-full-push  pkg/syncengine/reconcile.go 's|client\.Task\{ID: e\.TaskID, Name: mapper\.RemoteTitle\(li\)\}|client.Task{ID: e.TaskID, Name: mapper.RemoteTitle(li), Status: rt.Status}|' 'TestTitleOnlyDriftGetsNameOnlyUpdate' ./pkg/syncengine/
run_mut M12-ids-not-recorded pkg/syncengine/reconcile.go 's|res\.CreatedIDs\[e\.Key\] = created\.ID|_ = created|'            'TestApplyWithLimitAndNewestFirst' ./pkg/syncengine/
echo "mutations: $total, survivors/failures: $fail"
[ "$fail" -eq 0 ]
