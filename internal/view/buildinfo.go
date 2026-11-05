// Package view holds output types shared by the commands.
package view

// BuildInfo identifies a build of the CLI; main sets it through ldflags.
type BuildInfo struct {
	// Name is the binary name; NewRoot sets it from the command name when empty.
	Name      string `json:"name"`
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	GoVersion string `json:"goVersion"`
}
