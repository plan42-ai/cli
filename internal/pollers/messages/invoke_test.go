package messages

import (
	"testing"

	"github.com/plan42-ai/sdk-go/p42"
	sdkmessages "github.com/plan42-ai/sdk-go/p42/messages"
	"github.com/stretchr/testify/require"
)

func TestApplyProviderConfigSetsConfiguredOverrides(t *testing.T) {
	t.Parallel()

	req := &pollerInvokeAgentRequest{}
	p := &Poller{
		openAIEndpoint: "https://openai.example.test/v1",
		openAIToken:    "openai-token",
		claudeEndpoint: "https://claude.example.test",
		claudeToken:    "claude-token",
		modelMappings: sdkmessages.ModelMappings{
			p42.ModelTypeGpt51Codex: {Provider: "openai", Model: "gpt-5.1-codex-custom"},
		},
	}

	applyProviderConfig(req, p)

	require.NotNil(t, req.OpenAIEndpoint)
	require.Equal(t, p.openAIEndpoint, *req.OpenAIEndpoint)
	require.NotNil(t, req.OpenAIToken)
	require.Equal(t, p.openAIToken, *req.OpenAIToken)
	require.NotNil(t, req.ClaudeEndpoint)
	require.Equal(t, p.claudeEndpoint, *req.ClaudeEndpoint)
	require.NotNil(t, req.ClaudeToken)
	require.Equal(t, p.claudeToken, *req.ClaudeToken)
	require.Equal(t, p.modelMappings, req.ModelMappings)
}

func TestApplyProviderConfigLeavesAbsentOverridesUnset(t *testing.T) {
	t.Parallel()

	req := &pollerInvokeAgentRequest{}
	applyProviderConfig(req, &Poller{})

	require.Nil(t, req.OpenAIEndpoint)
	require.Nil(t, req.OpenAIToken)
	require.Nil(t, req.ClaudeEndpoint)
	require.Nil(t, req.ClaudeToken)
	require.Nil(t, req.ModelMappings)
}
