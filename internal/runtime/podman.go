package runtime

import (
	"context"
	"errors"
	"os/exec"
)

// Verify interface compliance at compile time.
var _ RuntimeProvider = (*PodmanProvider)(nil)

// PodmanProvider implements RuntimeProvider for Podman.
type PodmanProvider struct{}

// NewPodmanProvider creates a new PodmanProvider.
func NewPodmanProvider() *PodmanProvider {
	return &PodmanProvider{}
}

// Name returns "Podman".
func (p *PodmanProvider) Name() string {
	return "Podman"
}

// IsInstalled reports whether podman is available on the system.
func (p *PodmanProvider) IsInstalled() bool {
	path, err := exec.LookPath("podman")
	return path != "" && err == nil
}

// Validate checks that podman is installed and functional.
func (p *PodmanProvider) Validate(ctx context.Context) error {
	if !p.IsInstalled() {
		return errors.New("Podman is not installed on the local runner")
	}
	return nil
}

// PullImage pulls the specified container image.
func (p *PodmanProvider) PullImage(ctx context.Context, image string) error {
	panic("not implemented")
}

// RunContainer runs a container with the specified options.
func (p *PodmanProvider) RunContainer(ctx context.Context, opts ContainerOptions) error {
	panic("not implemented")
}

// ListJobs returns all jobs managed by this runtime.
func (p *PodmanProvider) ListJobs(ctx context.Context) ([]*Job, error) {
	panic("not implemented")
}

// KillJob terminates the job with the given ID.
func (p *PodmanProvider) KillJob(ctx context.Context, jobID string) error {
	panic("not implemented")
}
