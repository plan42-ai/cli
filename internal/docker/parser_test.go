package docker_test

import (
	"testing"

	"github.com/plan42-ai/plan42-cli/internal/docker"
	"github.com/plan42-ai/plan42-cli/internal/util"
	"github.com/stretchr/testify/require"
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
			value: "docker.io/ubuntu",
			expected: docker.ImageURI{
				Registry:   util.Pointer("docker.io"),
				Repository: "ubuntu",
			},
		},
		{
			name:  "explicit registry",
			value: "docker.io/docker.io/ubuntu",
			expected: docker.ImageURI{
				Registry:   util.Pointer("docker.io"),
				Repository: "docker.io/ubuntu",
			},
		},
		{
			name:  "registry port",
			value: "docker.io:443/ubuntu",
			expected: docker.ImageURI{
				Registry:     util.Pointer("docker.io"),
				RegistryPort: util.Pointer("443"),
				Repository:   "ubuntu",
			},
		},
		{
			name:  "namespace without registry",
			value: "foo/bar/baz",
			expected: docker.ImageURI{
				Repository: "foo/bar/baz",
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
			value: "foo/bar/baz:latest",
			expected: docker.ImageURI{
				Repository: "foo/bar/baz",
				Tag:        util.Pointer("latest"),
			},
		},
		{
			name:  "registry and tag",
			value: "docker.io/ubuntu:latest",
			expected: docker.ImageURI{
				Registry:   util.Pointer("docker.io"),
				Repository: "ubuntu",
				Tag:        util.Pointer("latest"),
			},
		},
		{
			name:  "repository with dot",
			value: "docker.io",
			expected: docker.ImageURI{
				Repository: "docker.io",
			},
		},
		{
			name:  "repository with dot and tag",
			value: "docker.io:443",
			expected: docker.ImageURI{
				Repository: "docker.io",
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
