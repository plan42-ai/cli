package poller

import (
	"github.com/plan42-ai/sdk-go/p42/messages"
)

type pollerInvokeAgentRequest struct {
	InvokePlatformFields
	messages.InvokeAgentRequest
}
