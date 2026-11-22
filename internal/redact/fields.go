package redact

// schemaFields are the object keys a location shows literally: field names of
// the Kubernetes, Gateway API, kgateway and Envoy schemas the rules work on.
// Every other key is user data and shows as [key] (design §7).
var schemaFields = setOf(
	// Kubernetes objects and workloads.
	"apiVersion", "kind", "metadata", "name", "generateName", "namespace", "uid", "resourceVersion",
	"generation", "creationTimestamp", "deletionTimestamp", "labels", "annotations", "ownerReferences",
	"finalizers", "managedFields", "spec", "status", "data", "stringData", "binaryData", "items",
	"conditions", "type", "reason", "message", "lastTransitionTime", "observedGeneration", "selector",
	"matchLabels", "matchExpressions", "key", "operator", "values", "value", "valueFrom", "template",
	"containers", "initContainers", "env", "envFrom", "image", "args", "command", "ports", "port",
	"containerPort", "protocol", "targetPort", "volumes", "volumeMounts", "resources", "limits",
	"requests", "replicas", "nodeSelector", "tolerations", "affinity", "serviceAccountName",
	"involvedObject", "source", "component", "host", "firstTimestamp", "lastTimestamp", "count",
	"action", "reportingController", "reportingInstance", "fieldPath", "secretKeyRef",
	"configMapKeyRef", "fieldRef", "optional", "path", "group", "extraLabels", "extraAnnotations",
	// Gateway API.
	"parentRefs", "sectionName", "hostnames", "rules", "matches", "headers", "queryParams", "method",
	"filters", "requestHeaderModifier", "responseHeaderModifier", "set", "add", "remove",
	"backendRefs", "weight", "timeouts", "listeners", "addresses", "gatewayClassName", "parents",
	"controllerName", "targetRefs", "targetRef", "urlRewrite", "requestRedirect", "requestMirror",
	"extensionRef",
	// kgateway.
	"headerModifiers", "request", "response", "basicAuth", "users", "transformation", "body",
	"default", "accessLog", "grpcService", "httpService", "initialMetadata", "httpSettings",
	"earlyRequestHeaderModifier", "tunnel", "extAuth", "authorizationRequest", "headersToAdd", "jwt",
	"jwks", "local", "inline", "remote", "providers", "issuer", "audiences", "kube", "envoyContainer",
	"deploymentOverlay", "podTemplate", "logName", "backendRef", "directResponse",
	// The key-name rule's words (design §7).
	"password", "secret", "token", "apiKey", "credential", "privateKey", "clientSecret",
	"sessionToken", "licenseKey",
	// Envoy config dump and admin JSON.
	"configs", "@type", "bootstrap", "node", "id", "cluster", "filter_metadata",
	"typed_filter_metadata", "static_listeners", "dynamic_listeners", "active_state", "warming_state",
	"listener", "filter_chains", "typed_config", "http_filters", "route_config",
	"dynamic_route_configs", "static_route_configs", "virtual_hosts", "routes", "match", "route",
	"domains", "request_headers_to_add", "response_headers_to_add", "headers_to_add", "header",
	"raw_value", "append_action", "mutations", "request_mutations", "response_mutations", "append",
	"initial_metadata", "grpc_service", "envoy_grpc", "cluster_name", "http_service", "server_uri",
	"authorization_request", "authorization_response", "source_codes", "default_source_code",
	"inline_code", "inline_string", "inline_bytes", "filename", "local_jwks", "remote_jwks",
	"requirement_map", "typed_per_filter_config", "typed_extension_protocol_options",
	"dynamic_active_clusters", "static_clusters", "dynamic_warming_clusters", "transport_socket",
	"common_tls_context", "tls_certificates", "private_key", "certificate_chain",
	"validation_context", "trusted_ca", "sni", "load_assignment", "endpoints", "lb_endpoints",
	"endpoint", "address", "socket_address", "port_value", "dynamic_active_secrets",
	"generic_secret", "tls_certificate", "last_updated", "version_info", "filter_config",
	"string_match", "exact_match", "prefix_match", "suffix_match", "contains_match",
	"safe_regex_match", "tunneling_config", "hostname", "header_mutation", "ext_authz",
	"connect_timeout", "certificates", "ca_cert", "cert_chain", "subject_alt_names", "dns", "uri",
	"serial_number", "days_until_expiration", "valid_from", "expiration_time",
)

func setOf(keys ...string) map[string]bool {
	m := make(map[string]bool, len(keys))
	for _, k := range keys {
		m[k] = true
	}
	return m
}
