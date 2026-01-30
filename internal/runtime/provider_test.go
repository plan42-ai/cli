package runtime

import (
	"context"
	"strings"
	"testing"
)

func TestNewProvider(t *testing.T) {
	tests := []struct {
		name    string
		runtime string
		wantErr bool
		wantName string
	}{
		{"empty defaults to apple", "", false, "Apple Container"},
		{"apple runtime", "apple", false, "Apple Container"},
		{"podman runtime", "podman", false, "Podman"},
		{"unknown runtime", "docker", true, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, err := NewProvider(tt.runtime)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewProvider(%q) error = %v, wantErr %v", tt.runtime, err, tt.wantErr)
				return
			}
			if !tt.wantErr && provider.Name() != tt.wantName {
				t.Errorf("NewProvider(%q).Name() = %q, want %q", tt.runtime, provider.Name(), tt.wantName)
			}
		})
	}
}

func TestAppleProviderValidate_ErrorMessage(t *testing.T) {
	provider := &AppleProvider{}
	err := provider.Validate(context.Background())
	// This test will pass if container is not installed (common case)
	// or if it is installed (no error)
	if err != nil && !strings.Contains(err.Error(), "Apple Container CLI is not installed") {
		t.Errorf("AppleProvider.Validate() error message = %q, want to contain installation hint", err.Error())
	}
}

func TestPodmanProviderValidate_ErrorMessage(t *testing.T) {
	provider := &PodmanProvider{}
	err := provider.Validate(context.Background())
	// This test will pass if podman is not installed (common case)
	// or if it is installed (no error)
	if err != nil && !strings.Contains(err.Error(), "Podman is not installed") {
		t.Errorf("PodmanProvider.Validate() error message = %q, want to contain installation hint", err.Error())
	}
}
