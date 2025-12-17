package poller

import (
	"context"

	"github.com/debugging-sucks/event-horizon-sdk-go/eh/messages"
)

type pollerPingRequest struct {
	messages.PingRequest
}

func (p *pollerPingRequest) Process(_ context.Context) messages.Message {
	// ping doesn't do anything except return a ping response.
	return &messages.PingResponse{}
}
