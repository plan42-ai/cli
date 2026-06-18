package messages

import (
	"github.com/plan42-ai/sdk-go/p42"
	"github.com/plan42-ai/sdk-go/p42/messages"
)

type pollerInvokeAgentRequest struct {
	InvokePlatformFields
	messages.InvokeAgentRequest
	client *p42.Client
}

func (req *pollerInvokeAgentRequest) shouldFetchPRFeedback() bool {
	if req == nil || req.Turn == nil {
		return false
	}
	if req.FeedBack != nil || req.PrivateGithubConnectionID == nil || req.SubAgent != nil {
		return false
	}
	return req.Turn.TurnIndex > 1
}
