#!/usr/bin/env bash
# Scale benchmark (design §6.4) on the live-check cluster.
#
#   hack/scale/run.sh apply   generate and apply the scale objects, wait for status
#   hack/scale/run.sh bench   time kgwctl 5 times; results in hack/live/out/scale/
#   hack/scale/run.sh clean   delete the scale namespaces and GatewayClass
#   hack/scale/run.sh         apply, then bench
#
# Env: KUBECONFIG_FILE (default hack/live/out/admin.kubeconfig), RUNS (default 5).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
OUT="$ROOT/hack/live/out/scale"
CONTEXT=kind-kgwctl-live
KUBECONFIG_FILE="${KUBECONFIG_FILE:-$ROOT/hack/live/out/admin.kubeconfig}"
RUNS="${RUNS:-5}"
BIN="$ROOT/hack/live/out/kgwctl"

k() { kubectl --kubeconfig "$KUBECONFIG_FILE" --context "$CONTEXT" "$@"; }
log() { echo ">> $*" >&2; }

# routes_with_status prints how many scale HTTPRoutes carry a status.parents entry.
routes_with_status() {
  k get httproutes -A -o jsonpath='{range .items[*]}{.metadata.namespace} {.status.parents[0].controllerName}{"\n"}{end}' |
    awk '$1 ~ /^scale-/ && $2 != "" { n++ } END { print n + 0 }'
}

apply() {
  mkdir -p "$OUT"
  (cd "$ROOT" && go run ./hack/scale >"$OUT/objects.yaml")
  local start=$SECONDS
  # Namespaces first, then the rest in one server-side apply.
  k apply --server-side -l kgwctl-scale=true -f "$OUT/objects.yaml" >/dev/null
  k apply --server-side -f "$OUT/objects.yaml" >/dev/null
  log "applied in $((SECONDS - start))s"

  # Wait (up to 15 min) for the controller to write status on all 5,000 routes.
  local n=0 deadline=$((SECONDS + 900))
  while [ "$SECONDS" -lt "$deadline" ]; do
    n="$(routes_with_status)"
    log "routes with status: $n/5000"
    [ "$n" -ge 5000 ] && break
    sleep 15
  done
  echo "settle: $n/5000 routes with status after $((SECONDS - start))s" >"$OUT/settle.txt"
}

# timed <name> <args...>: one run under /usr/bin/time -l; appends "<name> <real s> <peak RSS bytes>".
timed() {
  local name="$1"; shift
  /usr/bin/time -l "$BIN" --kubeconfig "$KUBECONFIG_FILE" --context "$CONTEXT" "$@" \
    >"$OUT/$name.stdout" 2>"$OUT/$name.time"
  awk -v n="$name" '/ real / { r = $1 } /maximum resident set size/ { m = $1 } END { print n, r, m }' \
    "$OUT/$name.time" | tee -a "$OUT/results.txt"
}

bench() {
  mkdir -p "$OUT"
  (cd "$ROOT" && go build -o "$BIN" ./cmd/kgwctl)
  : >"$OUT/results.txt"
  local i
  for i in $(seq 1 "$RUNS"); do
    timed "get-httproutes-$i" get httproutes -A
  done
  timed get-gateways get gateways -A
  timed describe-route describe httproute route-0 -n scale-0001
  log "rows in get httproutes -A: $(($(wc -l <"$OUT/get-httproutes-1.stdout") - 1))"
  awk '/^get-httproutes-/ { print $2, $3 }' "$OUT/results.txt" | sort -n |
    awk '{ t[NR] = $1; if ($2 > m) m = $2 } END {
      printf "get httproutes -A x%d: min %.2fs median %.2fs max %.2fs, peak RSS %.0f MiB\n",
        NR, t[1], t[int((NR + 1) / 2)], t[NR], m / 1048576 }' | tee "$OUT/summary.txt"
}

clean() {
  k delete gatewayclass -l kgwctl-scale=true --wait=false
  k delete namespace -l kgwctl-scale=true --wait=true --timeout=10m
}

case "${1:-all}" in
  apply) apply ;;
  bench) bench ;;
  clean) clean ;;
  all) apply; bench ;;
  *) echo "usage: $0 [apply|bench|clean]" >&2; exit 2 ;;
esac
