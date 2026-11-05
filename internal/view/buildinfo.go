// Package view holds output types shared by the commands.
package view

// BuildInfo identifies a build of the CLI; main sets it through ldflags.
type BuildInfo struct {
	Version, Commit, GoVersion string
}
