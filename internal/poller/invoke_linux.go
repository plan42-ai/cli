package poller

import (
	"context"

	"github.com/plan42-ai/cli/internal/util"
	"github.com/plan42-ai/sdk-go/p42/messages"
)

func (req *pollerInvokeAgentRequest) Process(_ context.Context) messages.Message {
	return &messages.InvokeAgentResponse{
		ErrorMessage: util.Pointer("Agent invocation has not yet been implemented for Linux runners"),
	}
}

func (req *pollerInvokeAgentRequest) Init(p *Poller) {
	_ = p
	// do nothing
}
