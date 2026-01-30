package runtime

import (
	"context"
	"io"
)

// ContainerOptions contains configuration for running a container.
type ContainerOptions struct {
	// ContainerID is the unique identifier for the container.
	ContainerID string

	// Image is the container image to run.
	Image string

	// CPUs is the number of CPUs to allocate.
	CPUs int

	// Memory is the memory limit (e.g., "8G").
	Memory string

	// Entrypoint overrides the default entrypoint.
	Entrypoint string

	// Args are the command arguments to pass to the entrypoint.
	Args []string

	// Stdin is the input stream for the container.
	Stdin io.Reader

	// Stdout is the output stream for the container.
	Stdout io.Writer

	// Stderr is the error stream for the container.
	Stderr io.Writer
}

// RuntimeProvider abstracts container runtime operations.
type RuntimeProvider interface {
	// Name returns the name of the runtime provider (e.g., "Apple", "Podman").
	Name() string

	// PullImage pulls a container image.
	PullImage(ctx context.Context, image string) error

	// RunContainer runs a container with the specified options.
	RunContainer(ctx context.Context, opts ContainerOptions) error
}
