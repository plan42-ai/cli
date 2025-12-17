package poller

import (
	"context"

	"github.com/debugging-sucks/event-horizon-sdk-go/eh/messages"
)

type pollerMessage interface {
	messages.Message
	Process(ctx context.Context) messages.Message
}
