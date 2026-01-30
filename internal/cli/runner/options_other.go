//go:build !darwin

package runner

import (
	"context"

	"github.com/plan42-ai/cli/internal/poller"
)

type PlatformOptions struct {
}

func (p *PlatformOptions) PollerOptions(options []poller.Option, _ string) []poller.Option {
	return options
}

func (p *PlatformOptions) Init(_ context.Context, _ string) error {
	return nil
}
