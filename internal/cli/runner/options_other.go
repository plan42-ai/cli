//go:build !darwin

package runner

import (
	"context"

	"github.com/plan42-ai/cli/internal/pollers/messages"
)

type PlatformOptions struct {
}

func (p *PlatformOptions) PollerOptions(options []messages.Option) []messages.Option {
	return options
}

func (p *PlatformOptions) Init(_ context.Context) error {
	return nil
}

func (p *PlatformOptions) SetupRuntime(runtimeName string) error {
	_ = runtimeName
	return nil
}
