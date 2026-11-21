#!/usr/bin/env bash
# Live check of kgwctl against a real kgateway install on kind.
#
#   hack/live-check.sh setup               create the cluster, install kgateway and the fixtures
#   hack/live-check.sh verify <kubeconfig> run kgwctl against it, outputs in hack/live/out/<name>/
#   hack/live-check.sh proxy               apply the proxy fixtures, check proxy dump with and without port-forward RBAC
#   hack/live-check.sh routes              check proxy routes live, from a dump file, and without the Role;
#                                          toggles the Helm value enableRouteSourceMetadata
#   hack/live-check.sh goldens             capture testdata/dumps/*.json; toggles the Helm value enableRouteSourceMetadata
#   hack/live-check.sh goldens-check       rerun the goldens' content (no credential material) and shape checks; offline
#
# verify also runs `check`, with two scenarios as admin: controller down, route to a missing Service.
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
PROXY_NS=kgwctl-proxy

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
write_reader_kubeconfig() { write_sa_kubeconfig default kgwctl-reader "$OUT/reader.kubeconfig"; }

# write_sa_kubeconfig <namespace> <serviceaccount> <file>: the token never reaches stdout.
write_sa_kubeconfig() {
  local ns="$1" sa="$2" file="$3" server ca token
  server="$(k config view --raw --minify -o jsonpath='{.clusters[0].cluster.server}')"
  ca="$(k config view --raw --minify -o jsonpath='{.clusters[0].cluster.certificate-authority-data}')"
  token="$(k -n "$ns" create token "$sa" --duration=24h)"
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
  - name: $sa
    user:
      token: $token
contexts:
  - name: $CONTEXT
    context:
      cluster: $CONTEXT
      user: $sa
current-context: $CONTEXT
EOF
  )
}

# Breaks the install twice, checks the report, restores. Reads dir, bin, kc and failed from verify (dynamic scope).
check_scenarios() {
  local replicas
  replicas="$(k -n "$KGW_NS" get deployment kgateway -o jsonpath='{.spec.replicas}')"
  restore_controller() { k -n "$KGW_NS" scale deployment kgateway --replicas="$replicas" >/dev/null; }
  restore_route() { k -n "$FIX_NS" delete httproute check-missing-svc --ignore-not-found >/dev/null; }
  trap 'restore_controller; restore_route' EXIT

  log "scenario: controller scaled to 0"
  k -n "$KGW_NS" scale deployment kgateway --replicas=0
  k -n "$KGW_NS" wait --for=delete pod -l app.kubernetes.io/name=kgateway --timeout=120s
  expect_exit controller-down.txt 1 "$kc" -n "$FIX_NS"
  # The controller is in $KGW_NS, not $FIX_NS: -n must still show its 005.
  grep -q 'KGWUI-005' "$dir/check-controller-down.txt" ||
    { log "FAIL check-controller-down.txt shows no KGWUI-005 under -n $FIX_NS"; failed=1; }
  restore_controller
  k -n "$KGW_NS" rollout status deployment kgateway --timeout=180s

  log "scenario: route to a missing Service"
  k apply -f - <<MANIFEST
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: check-missing-svc
  namespace: $FIX_NS
spec:
  parentRefs:
    - name: web
  rules:
    - backendRefs:
        - name: no-such-service
          port: 8080
MANIFEST
  k -n "$FIX_NS" wait --for=jsonpath='{.status.parents[0].controllerName}'=kgateway.dev/kgateway \
    httproute/check-missing-svc --timeout=120s
  expect_exit missing-service.txt 1 "$kc" -n "$FIX_NS"
  if ! grep -Eq "KGWUI-013 +HTTPRoute $FIX_NS/check-missing-svc" "$dir/check-missing-service.txt" ||
    ! grep -q 'no-such-service' "$dir/check-missing-service.txt"; then
    log "FAIL check-missing-service.txt shows no KGWUI-013 on check-missing-svc naming no-such-service"
    failed=1
  fi
  restore_route

  live_scenario
  trap - EXIT
}

# check --live while the missing Service of a route appears mid-run: 013 is gone from the final report
# and observed as cleared; exit 1 and exactly one JSON document.
live_scenario() {
  restore_live() {
    k -n "$FIX_NS" delete httproute check-live-svc --ignore-not-found >/dev/null
    k -n "$FIX_NS" delete service check-live-svc --ignore-not-found >/dev/null
  }
  trap 'restore_controller; restore_route; restore_live' EXIT

  log "scenario: check --live while a missing Service appears"
  k apply -f - <<MANIFEST
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: check-live-svc
  namespace: $FIX_NS
spec:
  parentRefs:
    - name: web
  rules:
    - backendRefs:
        - name: check-live-svc
          port: 8081
MANIFEST
  k -n "$FIX_NS" wait --for=jsonpath='{.status.parents[0].controllerName}'=kgateway.dev/kgateway \
    httproute/check-live-svc --timeout=120s

  local out="$dir/check-live.json" code=0 pid
  "$bin" --kubeconfig "$kc" --context "$CONTEXT" check --live --timeout 90s -n "$FIX_NS" -o json \
    >"$out" 2>"$out.stderr" &
  pid=$!
  sleep 20
  k apply -f - <<MANIFEST
apiVersion: v1
kind: Service
metadata:
  name: check-live-svc
  namespace: $FIX_NS
spec:
  ports:
    - port: 8081
      targetPort: 8081
MANIFEST
  wait "$pid" || code=$?
  echo "$code" >"$out.exit"
  log "check live: exit $code"
  # The fixtures' broken routes (orphan, bad-backend) keep error findings in $FIX_NS, and the admin reads every kind: exit 1.
  if [ "$code" -ne 1 ]; then
    log "FAIL check-live.json: exit $code, want 1"
    failed=1
  fi
  if [ "$(jq -s 'length' "$out" 2>/dev/null)" != 1 ]; then
    log "FAIL check-live.json is not exactly one JSON document"
    failed=1
  elif ! jq -e '.item.observation.stopReason == "timeout"' "$out" >/dev/null; then
    log "FAIL check-live.json: observation.stopReason is not timeout"
    failed=1
  else
    if jq -e --arg n check-live-svc \
      '[.item.installs[].findings[] | select(.code == "KGWUI-013") | select(.resource.name == $n)] | length > 0' \
      "$out" >/dev/null; then
      log "FAIL check-live.json still has KGWUI-013 on check-live-svc after the Service was created"
      failed=1
    fi
    # The run saw the route broken, then the fix: one 013 entry that cleared after it was first seen.
    if ! jq -e --arg n check-live-svc --arg ns "$FIX_NS" '
      def secs: (sub("\\.[0-9]+Z$"; "Z") | fromdateiso8601) + (((capture("(?<f>\\.[0-9]+)Z$") | .f) // "0") | tonumber);
      [.item.observation.observed[]
        | select(.code == "KGWUI-013" and .resource.name == $n and .resource.namespace == $ns)
        | select(.clearedAt != null and .lostAt == null)
        | select((.firstSeen | secs) <= (.clearedAt | secs))] | length == 1' \
      "$out" >/dev/null; then
      log "FAIL check-live.json: observation.observed lacks one KGWUI-013 on $FIX_NS/check-live-svc with firstSeen <= clearedAt"
      failed=1
    fi
  fi
  restore_live
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

  # check_run <file> <kubeconfig> <args...>: records stdout, stderr and the exit code as check-<file>*; returns the code.
  check_run() {
    local f="$1" kcfg="$2" code=0; shift 2
    "$bin" --kubeconfig "$kcfg" --context "$CONTEXT" check "$@" >"$dir/check-$f" 2>"$dir/check-$f.stderr" || code=$?
    echo "$code" >"$dir/check-$f.exit"
    log "check $f: exit $code"
    return "$code"
  }
  # expect_exit <file> <want> <kubeconfig> <args...>: runs check and fails verify on another exit code.
  expect_exit() {
    local f="$1" want="$2" kcfg="$3" code=0; shift 3
    check_run "$f" "$kcfg" "$@" || code=$?
    if [ "$code" -ne "$want" ]; then
      log "FAIL check-$f: exit $code, want $want"
      failed=1
    fi
  }

  # The fixtures hold broken routes on purpose, so exit 0, 1 and 2 are all recorded; 3 or more is a crash.
  local code=0
  check_run healthy.txt "$kc" -n "$FIX_NS" || code=$?
  [ "$code" -le 2 ] || { log "FAIL check-healthy.txt: exit $code"; failed=1; }
  code=0
  check_run healthy.json "$kc" -n "$FIX_NS" -o json || code=$?
  [ "$code" -le 2 ] || { log "FAIL check-healthy.json: exit $code"; failed=1; }
  # With docs/rbac.yaml no check may be unavailable because a read was denied.
  if grep -qi 'forbidden' "$dir/check-healthy.txt"; then
    log "FAIL check-healthy.txt mentions forbidden; see $dir"
    failed=1
  fi

  # The scenarios change the cluster, so they run once, with the admin kubeconfig.
  if [ "$kc" = "$KUBECONFIG_FILE" ]; then
    check_scenarios
  fi

  if ! awk -v ns="$FIX_NS" '$1 == ns && $2 == "orphan" { found = 1 } END { exit !found }' \
    "$dir/get-httproutes.table"; then
    log "FAIL orphan route $FIX_NS/orphan missing from get httproutes -A"
    failed=1
  fi
  [ "$failed" -eq 0 ] || { log "verify failed; outputs in $dir"; exit 1; }
  log "verify passed; outputs in $dir"
}

build() { mkdir -p "$OUT"; (cd "$ROOT" && go build -o "$OUT/kgwctl" ./cmd/kgwctl); }

# Applies the proxy fixtures and waits for Gateway "proxy" and its policies.
proxy_fixtures() {
  k apply -f "$ROOT/docs/rbac.yaml"
  k apply -f "$ROOT/hack/live/proxy-fixtures.yaml"
  k -n "$PROXY_NS" wait --for=condition=Programmed gateway/proxy --timeout=300s
  k -n "$PROXY_NS" rollout status deployment/proxy --timeout=300s
  k -n "$PROXY_NS" wait --for=jsonpath='{.status.ancestors[0].controllerName}'=kgateway.dev/kgateway \
    trafficpolicy/orders-transformation trafficpolicy/orders-extauth trafficpolicy/orders-ratelimit \
    trafficpolicy/public-no-extauth --timeout=120s
}

# proxy_dump <kubeconfig> <file> <namespace> <gateway>: stderr goes to <file>.stderr; returns kgwctl's exit code.
proxy_dump() {
  local code=0
  "$OUT/kgwctl" --kubeconfig "$1" --context "$CONTEXT" proxy dump "$4" -n "$3" >"$2" 2>"$2.stderr" || code=$?
  return "$code"
}

# proxy dump as reader plus the port-forward Role (exit 0, a ListenersConfigDump), then as reader only (exit 2).
proxy() {
  local dir="$OUT/proxy" op="$OUT/proxy-operator.kubeconfig" code failed=0
  build
  mkdir -p "$dir"
  proxy_fixtures
  write_reader_kubeconfig
  write_sa_kubeconfig "$PROXY_NS" kgwctl-proxy-operator "$op"

  code=0
  proxy_dump "$op" "$dir/dump-operator.json" "$PROXY_NS" proxy || code=$?
  log "proxy dump as kgwctl-proxy-operator: exit $code"
  if [ "$code" -ne 0 ]; then
    log "FAIL dump-operator.json: exit $code, want 0"
    failed=1
  elif ! jq -e '[.configs[]."@type" | select(endswith(".ListenersConfigDump"))] | length == 1' \
    "$dir/dump-operator.json" >/dev/null; then
    log "FAIL dump-operator.json is not JSON with a ListenersConfigDump"
    failed=1
  fi

  code=0
  proxy_dump "$OUT/reader.kubeconfig" "$dir/dump-reader.json" "$PROXY_NS" proxy || code=$?
  log "proxy dump as kgwctl-reader: exit $code"
  if [ "$code" -ne 2 ]; then
    log "FAIL dump-reader.json: exit $code, want 2"
    failed=1
  elif ! grep -q 'pods/portforward' "$dir/dump-reader.json.stderr"; then
    log "FAIL dump-reader.json.stderr does not name pods/portforward"
    failed=1
  fi
  [ "$failed" -eq 0 ] || { log "proxy failed; outputs in $dir"; exit 1; }
  log "proxy passed; outputs in $dir"
}

ROUTE_META=dev.kgateway.route_source
GOLDENS=(demo policies collide-meta)
# Golden content checks (no credential material), also recorded in testdata/dumps/README.md.
# (a) Secret entries hold only @type, name, *.filename or [redacted]; inline_bytes or inline_string always fails.
# shellcheck disable=SC2016
SECRETS_QUERY='[.configs[] | select((."@type" // "") | endswith(".SecretsConfigDump"))
  | ((.static_secrets // []) + (.dynamic_active_secrets // []) + (.dynamic_warming_secrets // []))[].secret
  | paths(scalars) as $p | ($p | map(tostring) | join(".")) as $k
  | select(($k | test("(^|\\.)inline_(bytes|string)$"))
      or (($k == "@type" or $k == "name" or ($k | test("(^|\\.)filename$")) or getpath($p) == "[redacted]") | not))]
  | length'
# (b) Any leaf whose key is exactly one of these is [redacted].
# shellcheck disable=SC2016
SENSITIVE_QUERY='[paths(scalars) as $p
  | select($p[-1] | IN("inline_bytes", "inline_string", "private_key", "password", "client_secret", "token"))
  | getpath($p) | select(. != "[redacted]")] | length'

# content_check <file>: prints counts only, never a matched value.
content_check() {
  local f="$1" secrets sensitive
  secrets="$(jq "$SECRETS_QUERY" "$f")"
  sensitive="$(jq "$SENSITIVE_QUERY" "$f")"
  log "content $(basename "$f"): secret-leaves=$secrets unredacted-sensitive=$sensitive"
  [ "$secrets" = 0 ] && [ "$sensitive" = 0 ]
}

# Golden shape checks, used to accept a capture and by goldens-check. JQ_DEFS: route lookup by
# virtual-host domain and route-name fragment, and the listener filter types by name.
# shellcheck disable=SC2016
JQ_DEFS='def vhosts: [.configs[] | select((."@type" // "") | endswith(".RoutesConfigDump"))
    | .dynamic_route_configs[]?.route_config.virtual_hosts[]?];
  def route($host; $frag): [vhosts[] | select(.domains | index([$host])) | .routes[]? | select(.name | contains($frag))];
  def filter_types: [.. | objects | select(has("http_filters")) | .http_filters[]
    | {key: .name, value: (.typed_config."@type" // "")}] | from_entries;
  def no_meta: tostring | contains("'"$ROUTE_META"'") | not;
  def collide: route("orders.kgwctl.example"; "httproute-orders-kgwctl-proxy-") as $a
    | route("orders-kgwctl.proxy.example"; "httproute-orders-kgwctl-proxy-") as $b
    | ($a | length) == 1 and ($b | length) == 1;
  def source($host): route($host; "httproute-orders-kgwctl-proxy-")[0].metadata.filter_metadata["'"$ROUTE_META"'"]
    | "\(.namespace)/\(.name)";'
# demo.json: team-a loaded all five demo hosts and their six routes.
# shellcheck disable=SC2016
SHAPE_DEMO='(vhosts | map(.domains[])) as $d
  | (["admin", "broken", "orders", "partner", "shop"] | map(. + ".team-a.example") | all(IN($d[])))
  and ([vhosts[].routes[]?] | length) >= 6 and no_meta'
# policies.json: both colliding routes; orders carries ext_authz, local rate limit and the fixture transformation.
# shellcheck disable=SC2016
SHAPE_POLICIES='filter_types as $ft | route("orders.kgwctl.example"; "httproute-orders-kgwctl-proxy-")[0] as $o
  | collide and no_meta
  and ([$o.typed_per_filter_config // {} | keys[] | $ft[.] // ""] as $t
    | ($t | any(endswith(".ext_authz.v3.ExtAuthz"))) and ($t | any(endswith(".local_ratelimit.v3.LocalRateLimit"))))
  and ($o.typed_per_filter_config | tostring | contains("x-kgwctl-route"))'
# collide-meta.json: each colliding route names its own HTTPRoute in route_source metadata.
SHAPE_META='collide and source("orders.kgwctl.example") == "kgwctl-proxy/orders"
  and source("orders-kgwctl.proxy.example") == "proxy/orders-kgwctl"'

# shape_check <file> <predicate>: true when the predicate holds.
shape_check() { jq -e "$JQ_DEFS $2" "$1" >/dev/null 2>&1; }

# Runs content_check and the shape checks on every golden; exits 1 if one fails.
goldens_check() {
  local f name var failed=0 dir="$ROOT/testdata/dumps"
  for f in "${GOLDENS[@]}"; do
    content_check "$dir/$f.json" || failed=1
  done
  for f in demo:SHAPE_DEMO policies:SHAPE_POLICIES collide-meta:SHAPE_META; do
    name="${f%%:*}" var="${f#*:}"
    if shape_check "$dir/$name.json" "${!var}"; then
      log "shape $name.json: ok"
    else
      log "FAIL shape $name.json: $var does not hold"
      failed=1
    fi
  done
  [ "$failed" -eq 0 ] || { log "goldens check failed: do not commit testdata/dumps"; exit 1; }
  log "goldens check passed"
}

# dump_until <file> <namespace> <gateway> <jq filter>: dumps as admin until the filter holds, up to 3 minutes.
dump_until() {
  local _
  for _ in $(seq 90); do
    if proxy_dump "$KUBECONFIG_FILE" "$1" "$2" "$3" && jq -e "$4" "$1" >/dev/null 2>&1; then
      rm -f "$1.stderr"
      return 0
    fi
    sleep 2
  done
  log "FAIL $(basename "$1"): $4 did not hold within 3 minutes"
  return 1
}

# route_meta <true|false>: sets the kgateway Helm value enableRouteSourceMetadata, keeping the other values.
route_meta() {
  local chart
  chart="$(h list -n "$KGW_NS" -o json | jq -r '.[] | select(.name == "kgateway") | .chart')"
  h upgrade kgateway oci://cr.kgateway.dev/kgateway-dev/charts/kgateway --version "${chart#kgateway-}" \
    -n "$KGW_NS" --reuse-values --set enableRouteSourceMetadata="$1" --wait --timeout 5m
}

# Captures the goldens as admin: demo use cases (team-a), proxy fixtures without and with route metadata.
goldens() {
  local dir="$ROOT/testdata/dumps" tmp="$OUT/goldens"
  build
  mkdir -p "$dir" "$tmp"
  if [ "$(h get values kgateway -n "$KGW_NS" -a -o json | jq '.enableRouteSourceMetadata')" != false ]; then
    log "enableRouteSourceMetadata is not false; set it back before capturing"
    exit 1
  fi
  proxy_fixtures

  dump_until "$dir/demo.json" team-a team-a "$JQ_DEFS $SHAPE_DEMO"
  dump_until "$dir/policies.json" "$PROXY_NS" proxy "$JQ_DEFS $SHAPE_POLICIES"

  trap 'route_meta false' EXIT
  route_meta true
  dump_until "$dir/collide-meta.json" "$PROXY_NS" proxy "$JQ_DEFS $SHAPE_META"
  route_meta false
  trap - EXIT
  dump_until "$tmp/meta-off-proxy.json" "$PROXY_NS" proxy "$JQ_DEFS $SHAPE_POLICIES"
  dump_until "$tmp/meta-off-team-a.json" team-a team-a "$JQ_DEFS $SHAPE_DEMO"
  log "enableRouteSourceMetadata back to false; no $ROUTE_META in the proxy or team-a dumps"

  log "kgateway $(h list -n "$KGW_NS" -o json | jq -r '.[] | select(.name == "kgateway") | .app_version')," \
    "Envoy $(k -n "$PROXY_NS" exec deploy/proxy -- wget -qO- 127.0.0.1:19000/server_info | jq -r .version)," \
    "captured $(date -u +%Y-%m-%d)"
  goldens_check
  log "goldens written to $dir"
}

ORDERS="$PROXY_NS/orders"
# The orders route's filters in request order, and its policy list, on the proxy fixtures.
ORDERS_FILTERS="ext_auth/kgwctl-proxy/authz ratelimit/local dynamic_modules/simple_mutations envoy.filters.http.router"
ORDERS_EXTAUTH="TrafficPolicy $PROXY_NS/orders-extauth (extAuth)"
ORDERS_POLICIES="policies: $ORDERS_EXTAUTH, TrafficPolicy $PROXY_NS/orders-ratelimit (rateLimit.local), TrafficPolicy $PROXY_NS/orders-transformation (transformation)"
# The vhosts of the two route names that both read as kgwctl-proxy/orders and proxy/orders-kgwctl.
COLLIDING=(orders-kgwctl_proxy_example orders_kgwctl_example)

# routes_run <file> <kubeconfig|-> <args...>: proxy routes, stdout to <file>, stderr to <file>.stderr,
# exit code to <file>.exit; "-" runs without a kubeconfig. Returns kgwctl's exit code.
routes_run() {
  local f="$1" kc="$2" code=0; shift 2
  if [ "$kc" = - ]; then
    "$OUT/kgwctl" proxy routes "$@" >"$f" 2>"$f.stderr" || code=$?
  else
    "$OUT/kgwctl" --kubeconfig "$kc" --context "$CONTEXT" proxy routes "$@" >"$f" 2>"$f.stderr" || code=$?
  fi
  echo "$code" >"$f.exit"
  log "proxy routes $(basename "$f"): exit $code"
  return "$code"
}

# expect_matched <file>: exit 0, orders matched, its filters in order, the ext_authz policy and the policy list.
expect_matched() {
  local f="$1" n order ok=0
  n="$(basename "$f")"
  order="$(awk '$1 ~ /^[0-9]+$/ { print $2 }' "$f" | paste -sd' ' -)"
  [ "$(cat "$f.exit")" = 0 ] || { log "FAIL $n: exit $(cat "$f.exit"), want 0"; ok=1; }
  grep -qF "· HTTPRoute $ORDERS · rule 0 · match 0" "$f" || { log "FAIL $n: $ORDERS not matched"; ok=1; }
  [ "$order" = "$ORDERS_FILTERS" ] || { log "FAIL $n: filters '$order', want '$ORDERS_FILTERS'"; ok=1; }
  grep -E '^ +1 ext_auth/kgwctl-proxy/authz +enabled by route ' "$f" | grep -qF "· $ORDERS_EXTAUTH" ||
    { log "FAIL $n: ext_auth entry does not name $ORDERS_EXTAUTH"; ok=1; }
  grep -qxF "      $ORDERS_POLICIES" "$f" || { log "FAIL $n: policy list differs"; ok=1; }
  if grep -q ambiguous "$f"; then log "FAIL $n: lists an ambiguous route"; ok=1; fi
  return "$ok"
}

# expect_ambiguous <file>: exit 0, no match, both colliding routes listed as ambiguous.
expect_ambiguous() {
  local f="$1" n v ok=0
  n="$(basename "$f")"
  [ "$(cat "$f.exit")" = 0 ] || { log "FAIL $n: exit $(cat "$f.exit"), want 0"; ok=1; }
  grep -qxF "no route matches HTTPRoute $ORDERS" "$f" || { log "FAIL $n: no 'no route matches' line"; ok=1; }
  for v in "${COLLIDING[@]}"; do
    grep -qxF "  listener~8080~$v-route-0-httproute-orders-kgwctl-proxy-0-0-matcher-0 · ambiguous: HTTPRoute $ORDERS, HTTPRoute proxy/orders-kgwctl" "$f" ||
      { log "FAIL $n: the $v route is not listed as ambiguous"; ok=1; }
  done
  if grep -q "filters (request order)" "$f"; then log "FAIL $n: shows a matched route"; ok=1; fi
  return "$ok"
}

# same_view <a> <b>: the two outputs are equal below the header line; a difference goes to <b>.diff.
same_view() {
  if diff <(tail -n +2 "$1") <(tail -n +2 "$2") >"$2.diff"; then
    rm -f "$2.diff"
  else
    log "FAIL $(basename "$2") differs from $(basename "$1") below the header; see $2.diff"
    return 1
  fi
}

# proxy routes on the orders HTTPRoute, as reader plus the port-forward Role, live and from a dump file,
# with route metadata on and off; then as reader only (exit 2). Toggles enableRouteSourceMetadata.
routes() {
  local dir="$OUT/routes" op="$OUT/proxy-operator.kubeconfig" failed=0 code value
  local sel=(--route "$ORDERS") file=(--gateway "$PROXY_NS/proxy" --route "$ORDERS")
  build
  mkdir -p "$dir"
  if [ "$(h get values kgateway -n "$KGW_NS" -a -o json | jq '.enableRouteSourceMetadata')" != false ]; then
    log "enableRouteSourceMetadata is not false; set it back before the routes check"
    exit 1
  fi
  proxy_fixtures
  write_reader_kubeconfig
  write_sa_kubeconfig "$PROXY_NS" kgwctl-proxy-operator "$op"

  trap 'route_meta false' EXIT
  route_meta true
  dump_until "$dir/meta-wait.json" "$PROXY_NS" proxy "$JQ_DEFS $SHAPE_META"
  routes_run "$dir/meta-live.txt" "$op" proxy -n "$PROXY_NS" "${sel[@]}" || true
  proxy_dump "$op" "$dir/meta-dump.json" "$PROXY_NS" proxy || { log "FAIL proxy dump with metadata"; failed=1; }
  routes_run "$dir/meta-file.txt" - --file "$dir/meta-dump.json" "${file[@]}" || true
  expect_matched "$dir/meta-live.txt" || failed=1
  expect_matched "$dir/meta-file.txt" || failed=1
  same_view "$dir/meta-live.txt" "$dir/meta-file.txt" || failed=1

  route_meta false
  trap - EXIT
  dump_until "$dir/off-wait.json" "$PROXY_NS" proxy "$JQ_DEFS $SHAPE_POLICIES"
  dump_until "$dir/off-team-a.json" team-a team-a "$JQ_DEFS $SHAPE_DEMO"
  log "enableRouteSourceMetadata back to false; no $ROUTE_META in the proxy or team-a dumps"
  routes_run "$dir/off-live.txt" "$op" proxy -n "$PROXY_NS" "${sel[@]}" || true
  proxy_dump "$op" "$dir/off-dump.json" "$PROXY_NS" proxy || { log "FAIL proxy dump without metadata"; failed=1; }
  routes_run "$dir/off-file.txt" - --file "$dir/off-dump.json" "${file[@]}" || true
  expect_ambiguous "$dir/off-live.txt" || failed=1
  expect_ambiguous "$dir/off-file.txt" || failed=1
  same_view "$dir/off-live.txt" "$dir/off-file.txt" || failed=1

  code=0
  routes_run "$dir/reader.txt" "$OUT/reader.kubeconfig" proxy -n "$PROXY_NS" "${sel[@]}" || code=$?
  if [ "$code" -ne 2 ]; then
    log "FAIL reader.txt: exit $code, want 2"
    failed=1
  elif ! grep -q 'create pods/portforward' "$dir/reader.txt.stderr"; then
    log "FAIL reader.txt.stderr does not name create pods/portforward"
    failed=1
  fi

  value="$(h get values kgateway -n "$KGW_NS" -a -o json | jq '.enableRouteSourceMetadata')"
  log "kgateway $(h list -n "$KGW_NS" -o json | jq -r '.[] | select(.name == "kgateway") | .app_version')," \
    "Envoy $(k -n "$PROXY_NS" exec deploy/proxy -- wget -qO- 127.0.0.1:19000/server_info | jq -r .version)," \
    "enableRouteSourceMetadata=$value"
  [ "$value" = false ] || { log "FAIL enableRouteSourceMetadata is $value"; failed=1; }
  [ "$failed" -eq 0 ] || { log "routes failed; outputs in $dir"; exit 1; }
  log "routes passed; outputs in $dir"
}

case "${1:-}" in
  setup) setup ;;
  verify) shift; verify "$@" ;;
  proxy) proxy ;;
  routes) routes ;;
  goldens) goldens ;;
  goldens-check) goldens_check ;;
  *) echo "usage: $0 setup | verify <kubeconfig> | proxy | routes | goldens | goldens-check" >&2; exit 2 ;;
esac
