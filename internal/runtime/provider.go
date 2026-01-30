// Package runtime provides abstractions for container runtimes.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
)

// RuntimeProvider defines the interface for container runtime implementations.
type RuntimeProvider interface {
	// Name returns the human-readable name of the runtime.
	Name() string
	// Validate checks if the runtime is installed and functional.
	Validate(ctx context.Context) error
}

// NewProvider creates a RuntimeProvider based on the runtime name from config.
// Valid values are "apple" and "podman".
func NewProvider(runtime string) (RuntimeProvider, error) {
	switch runtime {
	case "apple", "":
		return &AppleProvider{}, nil
	case "podman":
		return &PodmanProvider{}, nil
	default:
		return nil, fmt.Errorf("unknown runtime: %s (valid options: apple, podman)", runtime)
	}
}

// AppleProvider implements RuntimeProvider for Apple Container.
type AppleProvider struct{}

func (p *AppleProvider) Name() string {
	return "Apple Container"
}

func (p *AppleProvider) Validate(ctx context.Context) error {
	path, err := exec.LookPath("container")
	if err != nil || path == "" {
		return errors.New("Apple Container CLI is not installed. Install it via: brew install container")
	}
	return nil
}

// PodmanProvider implements RuntimeProvider for Podman.
type PodmanProvider struct{}

func (p *PodmanProvider) Name() string {
	return "Podman"
}

func (p *PodmanProvider) Validate(ctx context.Context) error {
	path, err := exec.LookPath("podman")
	if err != nil || path == "" {
		return errors.New("Podman is not installed on the local runner. Install it via: brew install podman")
	}
	return nil
}
