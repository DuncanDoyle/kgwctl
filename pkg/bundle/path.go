package bundle

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
)

// Fixed names from the archive layout. Paths are built from these and from
// components made by Name and File.
const (
	// Cluster is the namespace component of cluster-scoped objects.
	Cluster = "_cluster"
	// Core is the group component of the core API group.
	Core = "core"

	// ResourcesDir holds Gateway API and kgateway resources.
	ResourcesDir = "resources"
	// WorkloadsDir holds Deployments, Pods, Services and Events.
	WorkloadsDir = "workloads"
	// InstallDir holds one file per install.
	InstallDir = "install"
	// ControllerDir holds per-Pod controller captures.
	ControllerDir = "controller"
	// ProxiesDir holds per-Pod proxy captures.
	ProxiesDir = "proxies"
	// LogsDir holds container logs under a Pod directory.
	LogsDir = "logs"
	// ClusterDir holds cluster-wide facts.
	ClusterDir = "cluster"
	// CheckDir holds the check report.
	CheckDir = "check"

	// ManifestFile is the root manifest footer.
	ManifestFile = "manifest.json"
	// ErrorsFile is the root errors footer.
	ErrorsFile = "errors.txt"
	// RedactionsFile is the root redactions footer.
	RedactionsFile = "redactions.json"
	// RunLogFile is the root run log footer.
	RunLogFile = "run.log"

	// VersionFile is the cluster version file.
	VersionFile = "version.json"
	// CheckFile is the check report file.
	CheckFile = "check.json"
	// DeploymentsFile is the workload Deployments file.
	DeploymentsFile = "deployments.yaml"
	// PodsFile is the workload Pods file.
	PodsFile = "pods.yaml"
	// ServicesFile is the workload Services file.
	ServicesFile = "services.yaml"
	// EventsFile is the workload Events file.
	EventsFile = "events.yaml"
	// MetricsFile is the Prometheus metrics capture.
	MetricsFile = "metrics.txt"
	// ConfigDumpFile is the Envoy config dump.
	ConfigDumpFile = "config_dump.json"
	// StatsFile is the Envoy stats capture.
	StatsFile = "stats.txt"
	// ServerInfoFile is the Envoy server info capture.
	ServerInfoFile = "server_info.json"
	// CertsFile is the Envoy certificate summary.
	CertsFile = "certs.json"
	// ClustersFile is the Envoy clusters capture.
	ClustersFile = "clusters.txt"
)

const (
	maxComponent = 255
	maxDepth     = 8
)

// rootFooters are the reserved root names, written by the bundle itself.
var rootFooters = []string{ManifestFile, ErrorsFile, RedactionsFile, RunLogFile}

// constants are the fixed components a path may use besides names.
var constants = map[string]bool{
	Cluster: true, Core: true, ResourcesDir: true, WorkloadsDir: true, InstallDir: true,
	ControllerDir: true, ProxiesDir: true, LogsDir: true, ClusterDir: true, CheckDir: true,
	VersionFile: true, CheckFile: true, DeploymentsFile: true, PodsFile: true, ServicesFile: true,
	EventsFile: true, MetricsFile: true, ConfigDumpFile: true, StatsFile: true, ServerInfoFile: true,
	CertsFile: true, ClustersFile: true,
}

// exts are the file extensions a name may carry, longest first.
var exts = []Ext{PreviousLog, YAML, JSON, Log, Text}

// Path is a list of components joined under the archive root. Each component
// is a constant from this package or a real Kubernetes name, optionally with
// a file extension. Collectors pass names read from the API, never captured
// data such as log or response content.
type Path []string

// String returns the slash-joined path.
func (p Path) String() string { return strings.Join(p, "/") }

// NameKind selects the Kubernetes naming rule that Name applies.
type NameKind int

// Name kinds.
const (
	// Namespace is a DNS-1123 label.
	Namespace NameKind = iota
	// Object is a DNS-1123 subdomain.
	Object
	// ContainerName is a DNS-1123 label (Container is the replica type).
	ContainerName
	// Group is a DNS-1123 subdomain.
	Group
	// Kind is an API kind: a letter, then letters and digits.
	Kind
)

// String returns the kind's name.
func (k NameKind) String() string {
	switch k {
	case Namespace:
		return "namespace"
	case Object:
		return "object"
	case ContainerName:
		return "container"
	case Group:
		return "group"
	case Kind:
		return "kind"
	}
	return fmt.Sprintf("NameKind(%d)", int(k))
}

// Ext is a fixed file extension for File.
type Ext string

// File extensions.
const (
	// YAML is a YAML document.
	YAML Ext = ".yaml"
	// JSON is a JSON document.
	JSON Ext = ".json"
	// Log is a container log.
	Log Ext = ".log"
	// PreviousLog is a previous container instance's log.
	PreviousLog Ext = ".previous.log"
	// Text is plain text.
	Text Ext = ".txt"
)

func (e Ext) valid() bool {
	switch e {
	case YAML, JSON, Log, PreviousLog, Text:
		return true
	}
	return false
}

// Name validates s by the Kubernetes rule for kind and returns it for use as a
// path component. A failure wraps ErrInvalidPath.
func Name(kind NameKind, s string) (string, error) {
	var errs []string
	switch kind {
	case Namespace, ContainerName:
		errs = validation.IsDNS1123Label(s)
	case Object, Group:
		errs = validation.IsDNS1123Subdomain(s)
	case Kind:
		errs = validateKind(s)
	default:
		return "", fmt.Errorf("%w: unknown name kind %d", ErrInvalidPath, int(kind))
	}
	if len(errs) > 0 {
		return "", fmt.Errorf("%w: %s %q: %s", ErrInvalidPath, kind, s, strings.Join(errs, "; "))
	}
	return s, nil
}

func validateKind(s string) []string {
	if s == "" || len(s) > validation.DNS1035LabelMaxLength {
		return []string{"must be 1 to 63 characters"}
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		letter := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
		if !letter && (i == 0 || c < '0' || c > '9') {
			return []string{"must start with a letter and contain only letters and digits"}
		}
	}
	return nil
}

// File returns name plus ext as a path component. The name follows the object
// rule. When name plus ext passes 255 bytes, the name is cut and gets "-" and
// the first 12 hex digits of its SHA-256, so distinct names stay distinct.
// A failure wraps ErrInvalidPath.
func File(name string, ext Ext) (string, error) {
	if !ext.valid() {
		return "", fmt.Errorf("%w: unknown extension %q", ErrInvalidPath, string(ext))
	}
	if _, err := Name(Object, name); err != nil {
		return "", err
	}
	f := name + string(ext)
	if len(f) <= maxComponent {
		return f, nil
	}
	sum := sha256.Sum256([]byte(name))
	suffix := "-" + hex.EncodeToString(sum[:])[:hashDigits]
	// The cut keeps the prefix a subdomain: it ends in a letter or digit.
	prefix := strings.TrimRight(name[:maxComponent-len(ext)-len(suffix)], ".-")
	return prefix + suffix + string(ext), nil
}

// hashDigits is the length of the hash suffix File gives a cut name.
const hashDigits = 12

// ValidatePath checks a path against the archive rules: 1 to 8 components,
// each 1 to 255 bytes and either a constant of this package or a DNS-1123
// subdomain or kind name, optionally followed by one File extension. The
// first component may not be a reserved root footer name. '_' appears only in
// constants. Duplicates and conflicts are the writer's concern.
func ValidatePath(p Path) error {
	if len(p) == 0 || len(p) > maxDepth {
		return fmt.Errorf("%w: %d components, want 1 to %d", ErrInvalidPath, len(p), maxDepth)
	}
	for _, c := range p {
		if err := validComponent(c); err != nil {
			return err
		}
	}
	for _, r := range rootFooters {
		if strings.EqualFold(p[0], r) {
			return fmt.Errorf("%w: %q is reserved", ErrInvalidPath, p[0])
		}
	}
	return nil
}

func validComponent(c string) error {
	if c == "" || len(c) > maxComponent {
		return fmt.Errorf("%w: component length %d, want 1 to %d", ErrInvalidPath, len(c), maxComponent)
	}
	if constants[c] || isName(c) {
		return nil
	}
	for _, e := range exts {
		if base, ok := strings.CutSuffix(c, string(e)); ok && base != "" && isName(base) {
			return nil
		}
	}
	return fmt.Errorf("%w: component %q is not a name or constant", ErrInvalidPath, c)
}

// isName reports whether s is a DNS-1123 subdomain (labels included) or a kind.
func isName(s string) bool {
	return len(validation.IsDNS1123Subdomain(s)) == 0 || len(validateKind(s)) == 0
}
