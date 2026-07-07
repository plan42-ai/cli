package config

import (
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
	"github.com/plan42-ai/sdk-go/p42"
	"github.com/stretchr/testify/require"
)

func TestConfigDecodesRunnerModelMappings(t *testing.T) {
	t.Parallel()

	var cfg Config
	err := toml.NewDecoder(strings.NewReader(`
[runner]
url = "https://api.example.test"
token = "runner-token"

[runner.model_mappings."GPT-5.1 Codex"]
provider = "openai"
model = "gpt-5.1-codex-custom"
extended_context = true
`)).Decode(&cfg)

	require.NoError(t, err)
	require.Equal(t, "openai", cfg.Runner.ModelMappings[p42.ModelTypeGpt51Codex].Provider)
	require.Equal(t, "gpt-5.1-codex-custom", cfg.Runner.ModelMappings[p42.ModelTypeGpt51Codex].Model)
	require.True(t, cfg.Runner.ModelMappings[p42.ModelTypeGpt51Codex].ExtendedContext)
}

func TestConfigDecodesRunnerNoWebSearch(t *testing.T) {
	t.Parallel()

	var cfg Config
	err := toml.NewDecoder(strings.NewReader(`
	[runner]
	url = "https://api.example.test"
	token = "runner-token"
	no_websearch = true
	`)).Decode(&cfg)

	require.NoError(t, err)
	require.True(t, cfg.Runner.NoWebSearch)
}

func TestConfigDecodesRunnerNoCacheTTL(t *testing.T) {
	t.Parallel()

	var cfg Config
	err := toml.NewDecoder(strings.NewReader(`
			[runner]
			url = "https://api.example.test"
			token = "runner-token"
			no_cache_ttl = true
			`)).Decode(&cfg)

	require.NoError(t, err)
	require.True(t, cfg.Runner.NoCacheTTL)
}
