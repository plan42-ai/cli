package runner

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"

	"github.com/plan42-ai/cli/internal/poller"
)

type PlatformOptions struct {
	ContainerPath string `help:"Path to the container executable" default:"/opt/homebrew/bin/container"`
}

func (p *PlatformOptions) PollerOptions(options []poller.Option) []poller.Option {
	return append(
		options,
		poller.WithContainerPath(p.ContainerPath),
	)
}

func (p *PlatformOptions) Init(ctx context.Context) error {
	slog.InfoContext(ctx, "running `container system start`", "container_path", p.ContainerPath)
	// #nosec G204: ContainerPath is a local configuration value under user control with limited scope.
	cmd := exec.CommandContext(ctx, p.ContainerPath, "system", "start")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("container system start failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}
