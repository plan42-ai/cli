package githubevents

import (
	"context"
	"log/slog"

	"github.com/plan42-ai/concurrency"
	"github.com/plan42-ai/github-event-handlers/handlers"
)

const defaultWorkerCount = 100

// Dispatcher reads events from a channel and invokes HandlerRegistry.Handle
// via a fixed-size worker pool. It owns the worker goroutines and their
// lifecycle.
type Dispatcher struct {
	registry *handlers.HandlerRegistry
	cg       *concurrency.ContextGroup
	workerCg *concurrency.ContextGroup
	eventCh  <-chan handlers.Event
}

// NewDispatcher creates and starts a dispatcher that reads from eventCh. The
// returned dispatcher is fully running; there is no separate Start method.
// workerCount defaults to 100 if <= 0. The context from cg is passed to
// handlers; cancelling cg aborts in-flight handler calls (forced shutdown).
func NewDispatcher(registry *handlers.HandlerRegistry, cg *concurrency.ContextGroup, eventCh <-chan handlers.Event, workerCount int) *Dispatcher {
	if workerCount <= 0 {
		workerCount = defaultWorkerCount
	}
	d := &Dispatcher{
		registry: registry,
		cg:       cg,
		workerCg: concurrency.NewContextGroup(),
		eventCh:  eventCh,
	}
	for range workerCount {
		d.workerCg.Add(1)
		go d.worker()
	}
	return d
}

// Wait blocks until all workers have exited. Workers exit when eventCh is
// closed and drained, or when their context is cancelled.
func (d *Dispatcher) Wait(ctx context.Context) error {
	return d.workerCg.WaitContext(ctx)
}

func (d *Dispatcher) worker() {
	defer d.workerCg.Done()
	ctx := d.cg.Context()
	for evt := range d.eventCh {
		if err := d.registry.Handle(ctx, evt, nil); err != nil {
			slog.ErrorContext(ctx, "github events poller: handler error",
				"deliveryID", evt.GetDeliveryID(),
				"eventType", evt.EventType(),
				"error", err,
			)
		}
	}
}
