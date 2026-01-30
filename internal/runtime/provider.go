// Package runtime defines interfaces for container runtime providers.
// It enables the CLI to support multiple container runtimes (Apple container, Podman)
// through a common abstraction.
package runtime

import (
	"context"
	"io"
	"time"
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

	// RunJob runs a job with the specified options.
	RunJob(ctx context.Context, opts JobOptions) error

	// ListJobs returns all jobs managed by this runtime.
	ListJobs(ctx context.Context) ([]*Job, error)

	// KillJob terminates the job with the given ID.
	KillJob(ctx context.Context, jobID string) error
}

// JobOptions specifies the configuration for running a job.
type JobOptions struct {
	// JobID is the unique identifier for the job.
	JobID string

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

	// Stdin provides input to the job.
	Stdin io.Reader

	// Stdout receives standard output from the job.
	Stdout io.Writer

	// Stderr receives standard error from the job.
	Stderr io.Writer

	// LogPath is the path where job logs should be written.
	LogPath string
}

// Job represents a job managed by a runtime.
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
