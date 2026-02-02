//go:build !darwin

package poller

import containerruntime "github.com/plan42-ai/cli/internal/runtime"

type PlatformFields struct{}

type InvokePlatformFields struct{}

func WithRuntimeProvider(_ containerruntime.Provider) Option {
	return func(_ *Poller) {}
}
