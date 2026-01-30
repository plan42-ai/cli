package runtime

import (
	"context"
	"errors"
	"os/exec"
)

// Verify interface compliance at compile time.
var _ RuntimeProvider = (*AppleProvider)(nil)

// AppleProvider implements RuntimeProvider for Apple container.
type AppleProvider struct {
	containerPath string
}

// NewAppleProvider creates a new AppleProvider.
// containerPath specifies the path to the container executable.
func NewAppleProvider(containerPath string) *AppleProvider {
	if containerPath == "" {
		containerPath = "container"
	}
	return &AppleProvider{containerPath: containerPath}
}

// Name returns "Apple".
func (p *AppleProvider) Name() string {
	return "Apple"
}

// IsInstalled reports whether the Apple container CLI is available.
func (p *AppleProvider) IsInstalled() bool {
	path, err := exec.LookPath(p.containerPath)
	return path != "" && err == nil
}

// Validate checks that the Apple container CLI is installed.
func (p *AppleProvider) Validate(ctx context.Context) error {
	if !p.IsInstalled() {
		return errors.New("Apple container CLI is not installed. Install it with: brew install container")
	}
	return nil
}

// PullImage pulls the specified container image.
func (p *AppleProvider) PullImage(ctx context.Context, image string) error {
	panic("not implemented")
}

// RunContainer runs a container with the specified options.
func (p *AppleProvider) RunContainer(ctx context.Context, opts ContainerOptions) error {
	panic("not implemented")
}

// ListJobs returns all jobs managed by this runtime.
func (p *AppleProvider) ListJobs(ctx context.Context) ([]*Job, error) {
	panic("not implemented")
}

// KillJob terminates the job with the given ID.
func (p *AppleProvider) KillJob(ctx context.Context, jobID string) error {
	panic("not implemented")
}
