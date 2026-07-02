package messages

import (
	"github.com/plan42-ai/cli/internal/util"
	"github.com/plan42-ai/sdk-go/p42"
	"github.com/plan42-ai/sdk-go/p42/messages"
)

type pollerInvokeAgentRequest struct {
	InvokePlatformFields
	messages.InvokeAgentRequest
	client *p42.Client
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
}
