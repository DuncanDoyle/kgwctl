package app

import "github.com/spf13/cobra"

// CheckTestHooks exposes checkTestHooks to the external tests.
type CheckTestHooks = checkTestHooks

// NewRootWithCheckHooks is NewRoot with h passed to the check command.
func NewRootWithCheckHooks(cfg Config, h CheckTestHooks) *cobra.Command {
	return newRoot(cfg, h, proxyTestHooks{}, bundleTestHooks{})
}

// BundleTestHooks exposes bundleTestHooks to the external tests.
type BundleTestHooks = bundleTestHooks

// NewRootWithBundleHooks is NewRoot with h passed to the bundle command.
func NewRootWithBundleHooks(cfg Config, h BundleTestHooks) *cobra.Command {
	return newRoot(cfg, checkTestHooks{}, proxyTestHooks{}, h)
}

// ProxyTestHooks exposes proxyTestHooks to the external tests.
type ProxyTestHooks = proxyTestHooks

// NewRootWithProxyHooks is NewRoot with h passed to the proxy commands.
func NewRootWithProxyHooks(cfg Config, h ProxyTestHooks) *cobra.Command {
	return newRoot(cfg, checkTestHooks{}, h, bundleTestHooks{})
}

// RestoreOnCancel exposes restoreOnCancel to the external tests.
var RestoreOnCancel = restoreOnCancel
