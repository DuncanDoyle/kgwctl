package app

import "github.com/spf13/cobra"

// CheckTestHooks exposes checkTestHooks to the external tests.
type CheckTestHooks = checkTestHooks

// NewRootWithCheckHooks is NewRoot with h passed to the check command.
func NewRootWithCheckHooks(cfg Config, h CheckTestHooks) *cobra.Command { return newRoot(cfg, h) }

// DefaultOnCancel exposes defaultOnCancel to the external tests.
var DefaultOnCancel = defaultOnCancel
