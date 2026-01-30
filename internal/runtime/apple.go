package runtime

import (
	"context"
	"fmt"
	"os/exec"
)

// AppleProvider implements RuntimeProvider for Apple's container runtime.
type AppleProvider struct {
	// ContainerPath is the path to the container CLI executable.
	ContainerPath string
}

// NewAppleProvider creates a new AppleProvider with the specified container path.
func NewAppleProvider(containerPath string) *AppleProvider {
	return &AppleProvider{
		ContainerPath: containerPath,
	}
}

// Name returns the name of the runtime provider.
func (p *AppleProvider) Name() string {
	return "Apple"
}

// PullImage pulls a container image using Apple's container CLI.
func (p *AppleProvider) PullImage(ctx context.Context, image string) error {
	// #nosec G204: image is validated before this call
	cmd := exec.CommandContext(ctx, p.ContainerPath, "image", "pull", image)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to pull image: %w: %s", err, string(output))
	}
	return nil
}

// RunContainer runs a container using Apple's container CLI.
func (p *AppleProvider) RunContainer(ctx context.Context, opts ContainerOptions) error {
	args := []string{
		"run",
		"-c", fmt.Sprintf("%d", opts.CPUs),
		"-m", opts.Memory,
		"--name", opts.ContainerID,
		"-i",
		"--entrypoint", opts.Entrypoint,
		"--rm",
		opts.Image,
	}
	args = append(args, opts.Args...)

	// #nosec G204: arguments are validated before this call
	cmd := exec.CommandContext(ctx, p.ContainerPath, args...)
	cmd.Stdin = opts.Stdin
	cmd.Stdout = opts.Stdout
	cmd.Stderr = opts.Stderr

	return cmd.Run()
}
