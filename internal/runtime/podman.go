package runtime

import (
	"context"
	"fmt"
	"os/exec"
)

// PodmanProvider implements Provider for Podman container runtime.
type PodmanProvider struct {
	// PodmanPath is the path to the podman CLI executable.
	PodmanPath string
}

// NewPodmanProvider creates a new PodmanProvider with the specified podman path.
func NewPodmanProvider(podmanPath string) *PodmanProvider {
	return &PodmanProvider{
		PodmanPath: podmanPath,
	}
}

// Name returns the name of the runtime provider.
func (p *PodmanProvider) Name() string {
	return "Podman"
}

// PullImage pulls a container image using Podman.
func (p *PodmanProvider) PullImage(ctx context.Context, image string) error {
	// #nosec G204: image is validated before this call
	cmd := exec.CommandContext(ctx, p.PodmanPath, "pull", image)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to pull image: %w: %s", err, string(output))
	}
	return nil
}

// RunContainer runs a container using Podman.
func (p *PodmanProvider) RunContainer(ctx context.Context, opts ContainerOptions) error {
	args := []string{
		"run",
		"--cpus", fmt.Sprintf("%d", opts.CPUs),
		"--memory", opts.Memory,
		"--name", opts.ContainerID,
		"-i",
		"--entrypoint", opts.Entrypoint,
		"--rm",
		opts.Image,
	}
	args = append(args, opts.Args...)

	// #nosec G204: arguments are validated before this call
	cmd := exec.CommandContext(ctx, p.PodmanPath, args...)
	cmd.Stdin = opts.Stdin
	cmd.Stdout = opts.Stdout
	cmd.Stderr = opts.Stderr

	return cmd.Run()
}
