package poller

import (
	"context"
	"log/slog"

	"github.com/debugging-sucks/event-horizon-sdk-go/eh/messages"
)

type pollerPingRequest struct {
	messages.PingRequest
}

func (p *pollerPingRequest) Process(ctx context.Context) messages.Message {
	// ping doesn't do anything except return a ping response.
	slog.InfoContext(ctx, "received ping request")
	return &messages.PingResponse{}
}
