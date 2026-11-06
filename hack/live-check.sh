#!/usr/bin/env bash
# Live check of kgwctl against a real kgateway install on kind.
#
#   hack/live-check.sh setup               create the cluster, install kgateway and the fixtures
#   hack/live-check.sh verify <kubeconfig> run kgwctl against it, outputs in hack/live/out/<name>/
#
# Env: KUBECONFIG_FILE (admin kubeconfig, default hack/live/out/admin.kubeconfig),
# KGW_VERSION (default: latest stable kgateway release).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT="$ROOT/hack/live/out"
CLUSTER=kgwctl-live
CONTEXT="kind-$CLUSTER"
KUBECONFIG_FILE="${KUBECONFIG_FILE:-$OUT/admin.kubeconfig}"
KGW_NS=kgateway-system
FIX_NS=live

# Every cluster call names the kubeconfig file and context explicitly.
k() { kubectl --kubeconfig "$KUBECONFIG_FILE" --context "$CONTEXT" "$@"; }
h() { helm --kubeconfig "$KUBECONFIG_FILE" --kube-context "$CONTEXT" "$@"; }
log() { echo ">> $*" >&2; }

setup() {
  mkdir -p "$OUT"
  if kind get clusters | grep -qx "$CLUSTER"; then
    log "kind cluster $CLUSTER exists"
    kind export kubeconfig --name "$CLUSTER" --kubeconfig "$KUBECONFIG_FILE"
  else
    kind create cluster --name "$CLUSTER" --kubeconfig "$KUBECONFIG_FILE"
  fi

  local kgw gwapi
  kgw="${KGW_VERSION:-$(gh release view -R kgateway-dev/kgateway --json tagName --jq .tagName)}"
  # The Gateway API version kgateway is built against (AGENTS §14).
  gwapi="$(gh api "repos/kgateway-dev/kgateway/contents/go.mod?ref=$kgw" --jq .content | base64 -d |
    awk '$1 == "sigs.k8s.io/gateway-api" { print $2; exit }')"
  [ -n "$gwapi" ] || { echo "no sigs.k8s.io/gateway-api in go.mod of $kgw" >&2; exit 1; }
  log "kgateway $kgw, Gateway API $gwapi"
  printf 'kgateway=%s\ngateway-api=%s\n' "$kgw" "$gwapi" >"$OUT/versions.txt"

  k apply --server-side -f "https://github.com/kubernetes-sigs/gateway-api/releases/download/$gwapi/standard-install.yaml"
  h upgrade --install kgateway-crds oci://cr.kgateway.dev/kgateway-dev/charts/kgateway-crds \
    --version "$kgw" --namespace "$KGW_NS" --create-namespace --wait
  h upgrade --install kgateway oci://cr.kgateway.dev/kgateway-dev/charts/kgateway \
    --version "$kgw" --namespace "$KGW_NS" --create-namespace --wait --timeout 5m
  k wait --for=condition=Accepted gatewayclass/kgateway --timeout=120s

  k apply -f "$ROOT/hack/live/fixtures.yaml"
  k -n "$FIX_NS" wait --for=condition=Programmed gateway/web --timeout=300s
  # Give the controller time to write route and policy status.
  k -n "$FIX_NS" wait --for=jsonpath='{.status.parents[0].controllerName}'=kgateway.dev/kgateway \
    httproute/healthy httproute/bad-backend --timeout=120s
  k -n "$FIX_NS" wait --for=jsonpath='{.status.ancestors[0].controllerName}'=kgateway.dev/kgateway \
    trafficpolicy/healthy-policy --timeout=120s

  k apply -f "$ROOT/docs/rbac.yaml"
  write_reader_kubeconfig
  log "setup done: admin $KUBECONFIG_FILE, reader $OUT/reader.kubeconfig"
}

# Writes a kubeconfig for the kgwctl-reader ServiceAccount; the token never reaches stdout.
write_reader_kubeconfig() {
  local server ca token file="$OUT/reader.kubeconfig"
  server="$(k config view --raw --minify -o jsonpath='{.clusters[0].cluster.server}')"
  ca="$(k config view --raw --minify -o jsonpath='{.clusters[0].cluster.certificate-authority-data}')"
  token="$(k -n default create token kgwctl-reader --duration=24h)"
  (
    umask 077
    cat >"$file" <<EOF
apiVersion: v1
kind: Config
clusters:
  - name: $CONTEXT
    cluster:
      server: $server
      certificate-authority-data: $ca
users:
  - name: kgwctl-reader
    user:
      token: $token
contexts:
  - name: $CONTEXT
    context:
      cluster: $CONTEXT
      user: kgwctl-reader
current-context: $CONTEXT
EOF
  )
}

verify() {
  local kc="${1:?usage: verify <kubeconfig>}"
  [ -f "$kc" ] || { echo "no kubeconfig at $kc" >&2; exit 1; }
  kc="$(cd "$(dirname "$kc")" && pwd)/$(basename "$kc")"
  local name dir bin="$OUT/kgwctl" failed=0
  name="$(basename "$kc" .kubeconfig)"
  dir="$OUT/$name"
  mkdir -p "$dir"
  (cd "$ROOT" && go build -o "$bin" ./cmd/kgwctl)

  # run <file> <args...>: saves stdout to <file>, stderr to <file>.stderr.
  run() {
    local f="$1"; shift
    if "$bin" --kubeconfig "$kc" --context "$CONTEXT" "$@" >"$dir/$f" 2>"$dir/$f.stderr"; then
      log "ok   $f"
    else
      log "FAIL $f (exit $?): kgwctl $*"
      failed=1
    fi
  }

  run version.txt version
  run version.json version -o json
  local fmt
  for fmt in table wide json yaml; do
    run "get-all.$fmt" get all -A -o "$fmt"
  done
  run get-httproutes.table get httproutes -A
  run describe-gateway-web.txt describe gateway web -n "$FIX_NS"
  run describe-gatewayparameters-web-params.txt describe gatewayparameters web-params -n "$FIX_NS"
  run describe-trafficpolicy-healthy-policy.txt describe trafficpolicy healthy-policy -n "$FIX_NS"
  local r
  for r in healthy orphan bad-backend; do
    run "describe-httproute-$r.txt" describe httproute "$r" -n "$FIX_NS"
    run "describe-httproute-$r.json" describe httproute "$r" -n "$FIX_NS" -o json
  done

  if ! awk -v ns="$FIX_NS" '$1 == ns && $2 == "orphan" { found = 1 } END { exit !found }' \
    "$dir/get-httproutes.table"; then
    log "FAIL orphan route $FIX_NS/orphan missing from get httproutes -A"
    failed=1
  fi
  [ "$failed" -eq 0 ] || { log "verify failed; outputs in $dir"; exit 1; }
  log "verify passed; outputs in $dir"
}

case "${1:-}" in
  setup) setup ;;
  verify) shift; verify "$@" ;;
  *) echo "usage: $0 setup | verify <kubeconfig>" >&2; exit 2 ;;
esac
