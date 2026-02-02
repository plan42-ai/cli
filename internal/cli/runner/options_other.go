//go:build !darwin

package runner

import (
	"context"

	"github.com/plan42-ai/cli/internal/poller"
	containerruntime "github.com/plan42-ai/cli/internal/runtime"
)

type PlatformOptions struct {
	runtimeName     string
	runtimeProvider containerruntime.Provider
}

func (p *PlatformOptions) ConfigureRuntime(runtimeName string) error {
	p.runtimeName = runtimeName
	return nil
}

func (p *PlatformOptions) PollerOptions(options []poller.Option) []poller.Option {
	if p.runtimeProvider != nil {
		options = append(options, poller.WithRuntimeProvider(p.runtimeProvider))
	}
	return options
}

func (p *PlatformOptions) Init(_ context.Context) error {
	return nil
}
