// Package runtime defines interfaces for container runtime providers.
// It enables the CLI to support multiple container runtimes (Apple container, Podman)
// through a common abstraction.
package runtime

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/plan42-ai/sdk-go/p42"
)

// Runtime type constants.
const (
	RuntimeApple  = "apple"
	RuntimePodman = "podman"
)

// Provider defines the interface for container runtime implementations.
// Each supported runtime (Apple container, Podman) must implement this interface.
type Provider interface {
	// Name returns the human-readable name of the runtime (e.g., "Apple", "Podman").
	Name() string

	// IsInstalled reports whether the runtime is available on the system.
	IsInstalled() bool

	// Validate checks that the runtime is properly configured and functional.
	Validate(ctx context.Context) error

	// PullImage pulls the specified container image.
	PullImage(ctx context.Context, image string) error

	// RunContainer runs a container with the specified options.
	RunContainer(ctx context.Context, opts ContainerOptions) error

	// ListJobs returns all jobs managed by this runtime.
	ListJobs(ctx context.Context, opts ListJobsOptions) ([]*Job, error)

	// KillJob terminates the job with the given ID.
	KillJob(ctx context.Context, jobID string) error
}

// ContainerOptions specifies the configuration for running a container.
type ContainerOptions struct {
	// ContainerID is the unique identifier for the container.
	ContainerID string

	// Image is the container image to run.
	Image string

	// CPUs specifies the number of CPUs to allocate.
	CPUs int

	// Memory specifies the memory limit (in bytes).
	Memory int64

	// Entrypoint overrides the container's default entrypoint.
	Entrypoint string

	// Args are the arguments to pass to the entrypoint.
	Args []string

	// Stdin provides input to the container.
	Stdin io.Reader

	// Stdout receives standard output from the container.
	Stdout io.Writer

	// Stderr receives standard error from the container.
	Stderr io.Writer

	// LogPath is the path where container logs should be written.
	LogPath string
}

// ListJobsOptions configures how jobs are listed.
type ListJobsOptions struct {
	// All includes completed jobs in addition to running ones.
	All bool

	// Verbose enables verbose error logging.
	Verbose bool
}

// Job represents a container job managed by a runtime.
type Job struct {
	// CreatedDate is when the job was created.
	CreatedDate time.Time

	// TaskID is the Plan42 task ID associated with this job.
	TaskID string

	// TaskTitle is the human-readable title of the task.
	TaskTitle string

	// TurnIndex is the turn number within the task.
	TurnIndex int

	// Running indicates whether the job is currently executing.
	Running bool
}

// NewProvider creates a RuntimeProvider for the specified runtime type.
// If runtimeType is empty, it defaults to Apple runtime.
func NewProvider(runtimeType string, client *p42.Client, tenantID string) (RuntimeProvider, error) {
	if runtimeType == "" {
		runtimeType = RuntimeApple
	}

	switch runtimeType {
	case RuntimeApple:
		return NewAppleProvider(client, tenantID), nil
	case RuntimePodman:
		return nil, fmt.Errorf("podman runtime not yet implemented")
	default:
		return nil, fmt.Errorf("unknown runtime type: %s", runtimeType)
	}
}
