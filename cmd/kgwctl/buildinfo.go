package main

import (
	"runtime"
	"runtime/debug"

	"github.com/DuncanDoyle/kgwctl/pkg/cli"
)

// clientBuild returns the client build info. Values set through ldflags win;
// otherwise it falls back to the Go build info (module version, VCS revision).
func clientBuild(version, commit string, read func() (*debug.BuildInfo, bool)) cli.BuildInfo {
	if info, ok := read(); ok && info != nil {
		if version == "dev" && info.Main.Version != "" && info.Main.Version != "(devel)" {
			version = info.Main.Version
		}
		if commit == "unknown" {
			for _, s := range info.Settings {
				if s.Key == "vcs.revision" && s.Value != "" {
					commit = s.Value
				}
			}
		}
	}
	return cli.BuildInfo{Version: version, Commit: commit, GoVersion: runtime.Version()}
}
