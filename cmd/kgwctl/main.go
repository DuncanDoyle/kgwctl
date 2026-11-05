// Command kgwctl inspects kgateway installs and the Gateway API objects they serve.
package main

import (
	"os"
	"runtime"

	"github.com/DuncanDoyle/kgwctl/pkg/cli"
)

// Set through -ldflags "-X main.version=...".
var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	build := cli.BuildInfo{Version: version, Commit: commit, GoVersion: runtime.Version()}
	os.Exit(cli.Execute(cli.NewRootCommand(cli.WithBuildInfo(build))))
}
