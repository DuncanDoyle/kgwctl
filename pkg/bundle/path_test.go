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
	}
	for name, p := range bad {
		if err := ValidatePath(p); !errors.Is(err, ErrInvalidPath) {
			t.Errorf("%s: ValidatePath(%v) = %v, want ErrInvalidPath", name, p, err)
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
