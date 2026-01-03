package poller

import (
	"context"

	"github.com/plan42-ai/sdk-go/p42/messages"
)

type pollerMessage interface {
	messages.Message
	Init(p *Poller)
	Process(ctx context.Context) messages.Message
}
