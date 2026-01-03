//go:build !darwin

package runner

import "github.com/plan42-ai/cli/internal/poller"

type PlatformOptions struct {
}

func (p *PlatformOptions) PollerOptions(options []poller.Option) []poller.Option {
	return options
}
