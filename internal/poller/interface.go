package poller

import (
	"context"

	"github.com/plan42-ai/sdk-go/p42/messages"
)

type pollerMessage interface {
	messages.Message
	Process(ctx context.Context) messages.Message
}
