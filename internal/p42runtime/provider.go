// Package p42runtime defines interfaces for job runtime providers.
// It enables the CLI to support multiple runtimes (Apple container, Podman)
// through a common abstraction.
package p42runtime

import (
	"context"
	"io"
	"time"
)

const (
	RuntimeApple  = "apple"
	RuntimePodman = "podman"
)

// Provider defines the interface for job runtime implementations.
// Each supported runtime (Apple container, Podman) must implement this interface.
type Provider interface {
	// Name returns the configuration name (e.g., "apple", "podman") of the provider.
	Name() string
	// IsInstalled reports whether the runtime is available on the system.
	IsInstalled() bool

	// PullImage pulls the specified container image.
	PullImage(ctx context.Context, image string) error

	// RunJob runs a job with the specified options.
	RunJob(ctx context.Context, opts JobOptions) error

	// KillJob terminates the job with the given ID.
	KillJob(ctx context.Context, jobID string) error

	// GetRunningJobIDs returns IDs of all running jobs managed by this runtime.
	GetRunningJobIDs(ctx context.Context) ([]string, error)
	// GetAllJobIDs returns IDs of all jobs with log files (both running and completed).
	GetAllJobIDs(ctx context.Context) ([]string, error)

	// ValidateJobID checks if the given job ID is valid for this runtime.
	ValidateJobID(jobID string) error

	// DeleteJobLog removes the log file for the specified job.
	DeleteJobLog(jobID string) error
}

// JobOptions specifies the configuration for running a job.
type JobOptions struct {
	JobID      string
	Image      string
	CPUs       int // Required. Number of CPUs to allocate.
	MemoryInGB int // Required. Memory in whole gigabytes.
	Entrypoint string
	Args       []string
	Stdin      io.Reader
	Stdout     io.Writer
	Stderr     io.Writer
}

// Job represents a container job managed by a runtime.
type Job struct {
	TaskID      string
	TurnIndex   int
	Running     bool
	TaskTitle   string
	CreatedDate time.Time
}
