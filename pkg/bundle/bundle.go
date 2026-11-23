// Package bundle is the public contract between support-bundle collectors and
// the writer that stages their output. It imports no CLI code, so downstream
// builds can add collectors.
package bundle

import (
	"context"
	"fmt"
	"io"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

// Op identifies the operation that produced an item, as recorded in Source.
type Op int

// Operation codes. The zero Op is unset.
const (
	// OpList is a Kubernetes list or get.
	OpList Op = iota + 1
	// OpLog is a container log read.
	OpLog
	// OpPreviousLog is a previous-instance container log read.
	OpPreviousLog
	// OpMetrics is a Prometheus metrics read.
	OpMetrics
	// OpConfigDump is an Envoy config dump.
	OpConfigDump
	// OpConfigDumpEDS is an Envoy config dump with endpoints.
	OpConfigDumpEDS
	// OpStats is an Envoy stats read.
	OpStats
	// OpServerInfo is an Envoy server info read.
	OpServerInfo
	// OpCerts is an Envoy certs read.
	OpCerts
	// OpClusters is an Envoy clusters read.
	OpClusters
	// OpCheck is a check run.
	OpCheck
	// OpCustom is any other operation.
	OpCustom
)

var opNames = [...]string{
	OpList:          "list",
	OpLog:           "log",
	OpPreviousLog:   "previous-log",
	OpMetrics:       "metrics",
	OpConfigDump:    "config-dump",
	OpConfigDumpEDS: "config-dump-eds",
	OpStats:         "stats",
	OpServerInfo:    "server-info",
	OpCerts:         "certs",
	OpClusters:      "clusters",
	OpCheck:         "check",
	OpCustom:        "custom",
}

// String returns the operation's name.
func (o Op) String() string {
	if o < 0 || int(o) >= len(opNames) {
		return fmt.Sprintf("Op(%d)", int(o))
	}
	return opNames[o]
}

// MarshalText renders the operation as its name.
func (o Op) MarshalText() ([]byte, error) { return []byte(o.String()), nil }

// UnmarshalText parses an operation name; the empty string is the unset Op.
func (o *Op) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*o = 0
		return nil
	}
	for i, n := range opNames {
		if n == string(b) && n != "" {
			*o = Op(i)
			return nil
		}
	}
	return fmt.Errorf("unknown operation %q", b)
}

// CaptureIdentity records whether a Pod was still the same Pod when its
// capture finished.
type CaptureIdentity string

// Capture identities. The empty value is used for non-Pod items.
const (
	// IdentityVerified means the Pod's UID matched on re-read.
	IdentityVerified CaptureIdentity = "verified"
	// IdentityChanged means a different Pod now has the name.
	IdentityChanged CaptureIdentity = "changed"
	// IdentityGone means the Pod no longer exists.
	IdentityGone CaptureIdentity = "gone"
	// IdentityRereadFailed means the re-read failed.
	IdentityRereadFailed CaptureIdentity = "reread-failed"
)

// Source describes where an item or error came from. A write with Pod set
// must also set Namespace and the Pod's expected UID; the Sink then re-reads
// the Pod and sets Identity, which is output only.
type Source struct {
	Namespace string          `json:"namespace,omitempty"`
	Pod       string          `json:"pod,omitempty"`
	UID       types.UID       `json:"uid,omitempty"`
	Container string          `json:"container,omitempty"`
	Operation Op              `json:"operation,omitzero"`
	Identity  CaptureIdentity `json:"identity,omitempty"`
}

// OmitReason says why a capture was left out on purpose.
type OmitReason string

// Omission reasons.
const (
	// OmitNotRunning means the Pod is not running; the item is skipped.
	OmitNotRunning OmitReason = "not running"
	// OmitNoInstall means no install applies; the item is skipped.
	OmitNoInstall OmitReason = "no install"
	// OmitNoMetricsPort means the Pod declares no metrics port; the item is disabled.
	OmitNoMetricsPort OmitReason = "no metrics port"
)

// Sink receives collector output. Every method that returns an error returns
// ErrSealed after the collection deadline. Paths must be built from real
// Kubernetes names and constants (see Path); a write whose path fails
// ValidatePath, that sets Source.Identity, or that sets Source.Pod without
// Namespace and UID, returns ErrInvalidPath and is recorded as failed
// without the bad value.
//
// After the content of a write with Source.Pod is read, also when it was
// cut, the Sink re-reads the Pod, bounded by ctx and the request timeout,
// and records the CaptureIdentity with the item. A write that failed is not
// re-read. The identity never fails the item. A nil ctx is treated as
// context.Background(). Error drops a caller-set Source.Identity.
type Sink interface {
	// Object writes a Kubernetes object as YAML.
	Object(ctx context.Context, p Path, u *unstructured.Unstructured, src Source) error
	// ConfigDump writes an Envoy config dump.
	ConfigDump(ctx context.Context, p Path, r io.Reader, src Source) error
	// JSON writes a JSON document.
	JSON(ctx context.Context, p Path, r io.Reader, src Source) error
	// Prometheus writes Prometheus text.
	Prometheus(ctx context.Context, p Path, r io.Reader, src Source) error
	// Text writes plain text line by line.
	Text(ctx context.Context, p Path, r io.Reader, src Source) error
	// Omit records an item left out on purpose: skipped for OmitNotRunning
	// and OmitNoInstall, disabled for OmitNoMetricsPort. It writes nothing to
	// the archive, claims no path and never degrades the collector. An
	// invalid path, reason or Source.Identity returns ErrInvalidPath and
	// records nothing.
	Omit(p Path, src Source, reason OmitReason) error
	// Error records a failed item. It never fails.
	Error(item Path, src Source, err error)
	// Exclude counts a left-out object. It returns ErrSealed after the seal.
	// An empty group is core and is recorded as core; a non-empty group must
	// pass Name(Group), the kind must pass Name(Kind) and the reason must be
	// ExcludeOwnership or ExcludeScope, else ErrInvalidPath.
	Exclude(gk schema.GroupKind, reason ExcludeReason) error
}

// Collector gathers one kind of evidence into a Sink.
type Collector interface {
	// Name returns the collector's name; it must satisfy ValidName.
	Name() string
	// Collect writes the collector's items to s until ctx ends.
	Collect(ctx context.Context, env Env, s Sink) error
}

// ExcludeReason says why an object was left out of the bundle.
type ExcludeReason string

// Exclusion reasons.
const (
	// ExcludeNone keeps the object.
	ExcludeNone ExcludeReason = ""
	// ExcludeOwnership means another controller owns the object.
	ExcludeOwnership ExcludeReason = "ownership"
	// ExcludeScope means no selected install watches the object's namespace.
	ExcludeScope ExcludeReason = "scope"
)

// Env is what a collector receives for one run. The clients are guarded by the
// runner.
type Env struct {
	Kube      kubernetes.Interface
	Dynamic   dynamic.Interface
	Discovery discovery.DiscoveryInterface
	Scope     Scope
	Window    Window
	Replicas  []Replica
	// Now returns the run's clock.
	Now func() time.Time
	// RequestTimeout bounds each single request.
	RequestTimeout time.Duration
	// Keep is the ownership and scope filter. It returns ExcludeNone to keep
	// an object, and always does with AllControllers.
	Keep func(obj *unstructured.Unstructured) ExcludeReason
}

// Scope is the resolved scope of one run.
type Scope struct {
	// Namespace is the -n value; empty means all namespaces.
	Namespace string `json:"namespace,omitempty"`
	// Installs are the selected installs.
	Installs []Install `json:"installs,omitempty"`
	// AllControllers turns ownership and discovery scope off.
	AllControllers bool `json:"allControllers,omitempty"`
	// WorkloadNamespaces bounds workload collection; nil means all.
	WorkloadNamespaces        []string `json:"workloadNamespaces,omitempty"`
	InstallsOmitted           int      `json:"installsOmitted,omitempty"`
	WorkloadNamespacesOmitted int      `json:"workloadNamespacesOmitted,omitempty"`
}

// Namespaces returns nil for all namespaces, else the run's namespace followed
// by the install namespaces, without duplicates.
func (s Scope) Namespaces() []string {
	if s.Namespace == "" {
		return nil
	}
	out := []string{s.Namespace}
	seen := map[string]bool{s.Namespace: true}
	for _, in := range s.Installs {
		if !seen[in.Namespace] {
			seen[in.Namespace] = true
			out = append(out, in.Namespace)
		}
	}
	return out
}

// Install is one controller install in the run.
type Install struct {
	Namespace   string         `json:"namespace"`
	Deployment  string         `json:"deployment"`
	Chosen      string         `json:"chosen,omitempty"` // "flag" or "detected"
	MetricsPort int32          `json:"metricsPort,omitempty"`
	Discovery   DiscoveryScope `json:"discovery,omitzero"`
}

// DiscoveryScope records which namespaces an install watches.
type DiscoveryScope struct {
	// Mode is "all", "selectors", "unknown" or "namespaces-unreadable".
	Mode string `json:"mode"`
	// Selectors are the parsed selectors rendered by metav1.FormatLabelSelector,
	// at most 64.
	Selectors        []string `json:"selectors,omitempty"`
	SelectorsOmitted int      `json:"selectorsOmitted,omitempty"`
	Source           string   `json:"source,omitempty"`
	Reason           string   `json:"reason,omitempty"`
	// Unwatched are namespace names the install does not watch, bounded.
	Unwatched        []string `json:"unwatched,omitempty"`
	UnwatchedOmitted int      `json:"unwatchedOmitted,omitempty"`
}

// FormatSelector renders a parsed selector for DiscoveryScope.Selectors.
func FormatSelector(s *metav1.LabelSelector) string { return metav1.FormatLabelSelector(s) }

// Window bounds log collection.
type Window struct {
	Start time.Time `json:"start,omitzero"`
	Since time.Time `json:"since,omitzero"`
	Tail  int64     `json:"tail,omitempty"`
}

// Role is a replica's role.
type Role string

// Replica roles.
const (
	// RoleController is a controller Pod.
	RoleController Role = "controller"
	// RoleProxy is a gateway proxy Pod.
	RoleProxy Role = "proxy"
)

// Identity says how a replica was matched to its owner.
type Identity string

// Replica identities.
const (
	// IdentityAnnotation means an annotation named the owner.
	IdentityAnnotation Identity = "annotation"
	// IdentitySelector means a label selector matched.
	IdentitySelector Identity = "selector"
	// IdentityUnverified means the owner was not confirmed.
	IdentityUnverified Identity = "unverified"
)

// Container is one container of a replica.
type Container struct {
	Name     string `json:"name"`
	Init     bool   `json:"init,omitempty"`
	Restarts int32  `json:"restarts,omitempty"`
}

// Replica is one controller or proxy Pod in the run's inventory.
type Replica struct {
	Namespace           string          `json:"namespace"`
	Pod                 string          `json:"pod"`
	UID                 types.UID       `json:"uid,omitempty"`
	Role                Role            `json:"role"`
	Owner               string          `json:"owner,omitempty"`
	Identity            Identity        `json:"identity,omitempty"`
	Ready               bool            `json:"ready"`
	Terminating         bool            `json:"terminating,omitempty"`
	Phase               corev1.PodPhase `json:"phase,omitempty"`
	Containers          []Container     `json:"containers,omitempty"`
	StartTime           time.Time       `json:"startTime,omitzero"`
	MetricsPort         int32           `json:"metricsPort,omitempty"`
	MetricsPortDeclared bool            `json:"metricsPortDeclared,omitempty"`
}
