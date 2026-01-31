// Package runtime defines interfaces for job runtime providers.
// It enables the CLI to support multiple runtimes (Apple container, Podman)
// through a common abstraction.
package runtime

import (
	"context"
	"io"
	"time"
)

// Provider defines the interface for job runtime implementations.
// Each supported runtime (Apple container, Podman) must implement this interface.
type Provider interface {
	// Runtime identification and validation
	Name() string
	IsInstalled() bool
	Validate(ctx context.Context) error

	// Image management
	PullImage(ctx context.Context, image string) error

	// Job execution
	RunJob(ctx context.Context, opts JobOptions) error
	KillJob(ctx context.Context, jobID string) error

	// Job discovery (provider-specific - returns raw IDs only)
	GetRunningContainerIDs(ctx context.Context) ([]string, error)
	GetCompletedJobIDs() ([]string, error)

	// Job validation and cleanup
	ValidateJobID(jobID string) error
	DeleteJobLog(jobID string) error
}

// JobOptions specifies the configuration for running a job.
type JobOptions struct {
	JobID      string
	Image      string
	CPUs       int // Required. Number of CPUs to allocate.
	Memory     int // Required. Memory in whole gigabytes.
	Entrypoint string
	Args       []string
	Stdin      io.Reader
	Stdout     io.Writer
	Stderr     io.Writer
	LogPath    string
}

// Job represents a container job managed by a runtime.
type Job struct {
	TaskID      string
	TurnIndex   int
	Running     bool
	TaskTitle   string    // Populated by shared enrichment, not provider
	CreatedDate time.Time // Populated by shared enrichment, not provider
}
