package messages

import (
	"github.com/plan42-ai/cli/internal/util"
	"github.com/plan42-ai/sdk-go/p42"
	"github.com/plan42-ai/sdk-go/p42/messages"
)

type pollerInvokeAgentRequest struct {
	InvokePlatformFields
	messages.InvokeAgentRequest
	noWebSearch bool
	noCacheTTL  bool
	client      *p42.Client
}

func applyProviderConfig(req *pollerInvokeAgentRequest, p *Poller) {
	if p.openAIEndpoint != "" {
		req.OpenAIEndpoint = util.Pointer(p.openAIEndpoint)
	}
	if p.openAIToken != "" {
		req.OpenAIToken = util.Pointer(p.openAIToken)
	}
	if p.claudeEndpoint != "" {
		req.ClaudeEndpoint = util.Pointer(p.claudeEndpoint)
	}
	if p.claudeToken != "" {
		req.ClaudeToken = util.Pointer(p.claudeToken)
	}
	req.noWebSearch = p.noWebSearch
	req.noCacheTTL = p.noCacheTTL
	if len(p.modelMappings) > 0 {
		req.ModelMappings = p.modelMappings
	}
}

func (req *pollerInvokeAgentRequest) agentArgs() []string {
	args := []string{
		"--encrypted-input=false",
		"--plan42-proxy",
		"--log-agent-output",
	}
	if req.noWebSearch {
		args = append(args, "--no-websearch")
	}
	if req.noCacheTTL {
		args = append(args, "--no-cache-ttl")
	}
	return args
}
