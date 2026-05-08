package docker_test

import (
	"testing"

	"github.com/plan42-ai/cli/internal/docker"
	"github.com/plan42-ai/cli/internal/util"
	"github.com/stretchr/testify/require"
)

const (
	registryDocker      = "docker.io"
	repositoryUbuntu    = "ubuntu"
	repositoryFooBarBaz = "foo/bar/baz"
)

func TestSuccess(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name     string
		value    string
		expected docker.ImageURI
	}{
		{
			name:  "infer registry and tag",
			value: registryDocker + "/" + repositoryUbuntu,
			expected: docker.ImageURI{
				Registry:   util.Pointer(registryDocker),
				Repository: repositoryUbuntu,
			},
		},
		{
			name:  "explicit registry",
			value: registryDocker + "/" + registryDocker + "/" + repositoryUbuntu,
			expected: docker.ImageURI{
				Registry:   util.Pointer(registryDocker),
				Repository: registryDocker + "/" + repositoryUbuntu,
			},
		},
		{
			name:  "registry port",
			value: registryDocker + ":443/" + repositoryUbuntu,
			expected: docker.ImageURI{
				Registry:     util.Pointer(registryDocker),
				RegistryPort: util.Pointer("443"),
				Repository:   repositoryUbuntu,
			},
		},
		{
			name:  "namespace without registry",
			value: repositoryFooBarBaz,
			expected: docker.ImageURI{
				Repository: repositoryFooBarBaz,
			},
		},
		{
			name:  "repository with tag",
			value: "foo:latest",
			expected: docker.ImageURI{
				Repository: "foo",
				Tag:        util.Pointer("latest"),
			},
		},
		{
			name:  "repository namespace and tag 1",
			value: "foo/bar:latest",
			expected: docker.ImageURI{
				Repository: "foo/bar",
				Tag:        util.Pointer("latest"),
			},
		},
		{
			name:  "repository namespace and tag 2",
			value: repositoryFooBarBaz + ":latest",
			expected: docker.ImageURI{
				Repository: repositoryFooBarBaz,
				Tag:        util.Pointer("latest"),
			},
		},
		{
			name:  "registry and tag",
			value: registryDocker + "/" + repositoryUbuntu + ":latest",
			expected: docker.ImageURI{
				Registry:   util.Pointer(registryDocker),
				Repository: repositoryUbuntu,
				Tag:        util.Pointer("latest"),
			},
		},
		{
			name:  "repository with dot",
			value: registryDocker,
			expected: docker.ImageURI{
				Repository: registryDocker,
			},
		},
		{
			name:  "repository with dot and tag",
			value: registryDocker + ":443",
			expected: docker.ImageURI{
				Repository: registryDocker,
				Tag:        util.Pointer("443"),
			},
		},
	}

	for _, tc := range testCases {
		t.Run(
			tc.name, func(t *testing.T) {
				t.Parallel()
				actual, err := docker.ParseImageURI(tc.value)
				require.NoError(t, err)
				require.NotNil(t, actual)
				require.Equal(t, tc.expected, *actual)
			},
		)
	}
}

func TestErrors(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name          string
		value         string
		expectedError string
	}{
		{
			name:          "bad tag",
			value:         "docker.io/ubuntu:latest extra text",
			expectedError: "invalid tag: 'latest extra text'",
		},
		{
			name:          "bad registry",
			value:         "docker.io_/ubuntu",
			expectedError: "invalid registry: 'docker.io_'",
		},
		{
			name:          "bad repository",
			value:         "ubuntu+=5:latest",
			expectedError: "invalid repository: 'ubuntu+=5'",
		},
		{
			name:          "bad port 1",
			value:         "docker.io:443a/ubuntu",
			expectedError: "invalid port: '443a'",
		},
		{
			name:          "bad port 2",
			value:         "docker.io:65537/ubuntu",
			expectedError: "invalid port: '65537'",
		},
	}

	for _, tc := range testCases {
		t.Run(
			tc.name, func(t *testing.T) {
				t.Parallel()
				actual, err := docker.ParseImageURI(tc.value)
				require.Error(t, err)
				require.Nil(t, actual)
				require.Equal(t, tc.expectedError, err.Error())
			},
		)
	}
}
