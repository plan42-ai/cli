package poller

import (
	"context"

	"github.com/debugging-sucks/event-horizon-sdk-go/eh/messages"
	"github.com/debugging-sucks/runner/internal/util"
)

func (p *pollerInvokeAgentRequest) Process(_ context.Context) messages.Message {
	return &messages.InvokeAgentResponse{
		ErrorMessage: util.Pointer("Agent invocation has not yet been implemented for Linux runners"),
	}
}
