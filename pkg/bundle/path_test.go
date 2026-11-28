package bundle

import (
	"errors"
	"strings"
	"testing"
)

func TestPathConstructors(t *testing.T) {
	if s, err := Name(Group, "gateway.networking.k8s.io"); err != nil || s != "gateway.networking.k8s.io" {
		t.Fatalf("group: %q, %v", s, err)
	}
	if _, err := Name(Kind, "HTTPRoute"); err != nil {
		t.Fatalf("kind: %v", err)
	}
	if s, err := File("gw", YAML); err != nil || s != "gw.yaml" {
		t.Fatalf("file: %q, %v", s, err)
	}
	if s, err := File("app", PreviousLog); err != nil || s != "app.previous.log" {
		t.Fatalf("previous log: %q, %v", s, err)
	}
	bad := []struct {
		kind NameKind
		s    string
	}{
		{Object, "a/b"},
		{Namespace, "Team"},
		{ContainerName, "x_y"},
		{Object, strings.Repeat("a", 254)},
		{Kind, "1Route"},
		{Group, "Bad_Group"},
	}
	for _, b := range bad {
		if _, err := Name(b.kind, b.s); !errors.Is(err, ErrInvalidPath) {
			t.Errorf("Name(%v, %q) = %v, want ErrInvalidPath", b.kind, b.s, err)
		}
	}
	if _, err := File("a/b", YAML); !errors.Is(err, ErrInvalidPath) {
		t.Errorf("File with slash: %v", err)
	}
	if _, err := File("gw", Ext("")); !errors.Is(err, ErrInvalidPath) {
		t.Errorf("File with unknown ext: %v", err)
	}
}

// TestFileLongName: a name that does not fit with its extension gets a
// bounded, valid and distinct file name; one that fits is unchanged.
func TestFileLongName(t *testing.T) {
	long := strings.Repeat("a", 61) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 63)
	if len(long) != 253 {
		t.Fatalf("fixture name is %d bytes", len(long))
	}
	other := long[:252] + "e"
	seen := map[string]string{}
	for _, name := range []string{long, other} {
		for _, ext := range []Ext{YAML, PreviousLog} {
			f, err := File(name, ext)
			if err != nil {
				t.Fatalf("File(%d bytes, %s): %v", len(name), ext, err)
			}
			if len(f) > 255 || !strings.HasSuffix(f, string(ext)) || !strings.HasPrefix(f, name[:200]) {
				t.Errorf("File(%d bytes, %s) = %q (%d bytes)", len(name), ext, f, len(f))
			}
			if err := ValidatePath(Path{ResourcesDir, "x", f}); err != nil {
				t.Errorf("ValidatePath(%q): %v", f, err)
			}
			if _, dup := seen[f]; dup {
				t.Errorf("two names share file name %q", f)
			}
			seen[f] = name
			if again, _ := File(name, ext); again != f {
				t.Errorf("File is not stable: %q then %q", f, again)
			}
		}
	}
	// A '.' at the cut point is dropped, so the result stays a subdomain.
	dotAtCut := strings.Repeat("a", 236) + "." + strings.Repeat("b", 16)
	if f, err := File(dotAtCut, YAML); err != nil || ValidatePath(Path{"x", f}) != nil {
		t.Errorf("File(dot at cut) = %q, %v", f, err)
	}
	fits := strings.Repeat("a", 250)
	if f, err := File(fits, YAML); err != nil || f != fits+".yaml" {
		t.Errorf("File(250 bytes) = %q, %v; want unchanged", f, err)
	}
}

func TestValidatePath(t *testing.T) {
	ok := Path{ResourcesDir, Core, "Service", Cluster, "x.yaml"}
	if err := ValidatePath(ok); err != nil {
		t.Fatalf("valid path rejected: %v", err)
	}
	if got := ok.String(); got != "resources/core/Service/_cluster/x.yaml" {
		t.Errorf("String() = %q", got)
	}
	nine := make(Path, 9)
	for i := range nine {
		nine[i] = "a"
	}
	bad := map[string]Path{
		"empty":        {},
		"dot":          {"."},
		"dotdot":       {"a", ".."},
		"hidden":       {".hidden"},
		"dash":         {"-x"},
		"nine":         nine,
		"reserved":     {"manifest.json"},
		"reservedCase": {"Run.Log"},
		"slash":        {"a/b"},
		"empty comp":   {"a", ""},
		"long":         {strings.Repeat("a", 256)},
		"underscore":   {"x", "arbitrary_value"},
		"mixed":        {"x", "Foo-Bar"},
		"cluster-like": {"x", "_cluster2"},
		"bare-ext":     {"x", ".log"},
		"space":        {"x", "a b.json"},
	}
	for name, p := range bad {
		if err := ValidatePath(p); !errors.Is(err, ErrInvalidPath) {
			t.Errorf("%s: ValidatePath(%v) = %v, want ErrInvalidPath", name, p, err)
		}
	}
	for _, p := range []Path{
		{ProxiesDir, "ns", "gw-0.x", ConfigDumpFile},
		{ControllerDir, "ns", "p", LogsDir, "kgateway.previous.log"},
		{ResourcesDir, "gateway.networking.k8s.io", "HTTPRoute", "ns", "r.yaml"},
		{"X", "F.txt"},
	} {
		if err := ValidatePath(p); err != nil {
			t.Errorf("ValidatePath(%v) = %v", p, err)
		}
	}
	if err := ValidatePath(nine[:8]); err != nil {
		t.Errorf("8 components rejected: %v", err)
	}
}

func TestValidName(t *testing.T) {
	for _, s := range []string{"logs", "proxy-admin"} {
		if err := ValidName(s); err != nil {
			t.Errorf("ValidName(%q) = %v", s, err)
		}
	}
	for _, s := range []string{"Logs", "a_b", "-x", ""} {
		if err := ValidName(s); err == nil {
			t.Errorf("ValidName(%q) accepted", s)
		}
	}
}

func TestScopeNamespaces(t *testing.T) {
	if got := (Scope{}).Namespaces(); got != nil {
		t.Errorf("all namespaces = %v, want nil", got)
	}
	s := Scope{Namespace: "a", Installs: []Install{{Namespace: "kgw"}, {Namespace: "a"}, {Namespace: "kgw"}}}
	got := s.Namespaces()
	if len(got) != 2 || got[0] != "a" || got[1] != "kgw" {
		t.Errorf("Namespaces() = %v", got)
	}
}

func TestOpString(t *testing.T) {
	if OpList.String() != "list" || OpPreviousLog.String() != "previous-log" || Op(999).String() == "" {
		t.Errorf("unexpected Op strings: %q %q %q", OpList, OpPreviousLog, Op(999))
	}
}
