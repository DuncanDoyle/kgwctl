package main

import (
	"runtime"
	"runtime/debug"
	"testing"
)

func readInfo(mainVersion string, settings ...debug.BuildSetting) func() (*debug.BuildInfo, bool) {
	return func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{
			Main:     debug.Module{Version: mainVersion},
			Settings: settings,
		}, true
	}
}

func TestClientBuildLdflags(t *testing.T) {
	got := clientBuild("v9.9.9", "abc", readInfo("v0.1.0"))
	if got.Version != "v9.9.9" || got.Commit != "abc" {
		t.Fatalf("got %+v, want ldflags values", got)
	}
	if got.GoVersion != runtime.Version() {
		t.Fatalf("GoVersion = %q, want %q", got.GoVersion, runtime.Version())
	}
}

func TestClientBuildModuleVersion(t *testing.T) {
	got := clientBuild("dev", "unknown", readInfo("v0.1.0"))
	if got.Version != "v0.1.0" || got.Commit != "unknown" {
		t.Fatalf("got %+v, want v0.1.0/unknown", got)
	}
}

func TestClientBuildVCS(t *testing.T) {
	got := clientBuild("dev", "unknown", readInfo("(devel)", debug.BuildSetting{Key: "vcs.revision", Value: "1234abcd"}))
	if got.Version != "dev" || got.Commit != "1234abcd" {
		t.Fatalf("got %+v, want dev/1234abcd", got)
	}
}

func TestClientBuildNoInfo(t *testing.T) {
	got := clientBuild("dev", "unknown", func() (*debug.BuildInfo, bool) { return nil, false })
	if got.Version != "dev" || got.Commit != "unknown" {
		t.Fatalf("got %+v, want dev/unknown", got)
	}
}
