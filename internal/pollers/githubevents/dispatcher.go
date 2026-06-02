package githubevents

import (
	"context"
	"log/slog"
	"time"

	"github.com/plan42-ai/concurrency"
	ghapi "github.com/plan42-ai/github-event-handlers/github"
	"github.com/plan42-ai/github-event-handlers/handlers"
)

const defaultWorkerCount = 100

// DispatchItem pairs an event with the github client a handler should use to act
// on it. The client is authenticated for the event's connection.
type DispatchItem struct {
	event  handlers.Event
	client *ghapi.Client
}

// Dispatcher reads events from a channel and invokes HandlerRegistry.Handle
// via a fixed-size worker pool. It owns a single ContextGroup that tracks the
// worker goroutines and provides the context passed to handlers.
type Dispatcher struct {
	registry *handlers.HandlerRegistry
	cg       *concurrency.ContextGroup
	eventCh  <-chan DispatchItem
}

func NewDispatcher(registry *handlers.HandlerRegistry, eventCh <-chan DispatchItem, workerCount int) *Dispatcher {
	if workerCount <= 0 {
		workerCount = defaultWorkerCount
	}
	d := &Dispatcher{
		registry: registry,
		cg:       concurrency.NewContextGroup(),
		eventCh:  eventCh,
	}
	for range workerCount {
		d.cg.Add(1)
		go d.worker()
	}
	return d
}

// ShutdownContext waits for all workers to drain the event channel and exit,
// bounded by ctx. Workers exit once eventCh is closed and drained, so the
// producer must close the channel first. The worker context stays live for the
// whole wait so in-flight handlers can finish. On a clean drain it cancels the
// context group to release it. If ctx's deadline is exceeded first it returns
// the error without cancelling; call Close to force the workers to stop.
func (d *Dispatcher) ShutdownContext(ctx context.Context) error {
	if err := d.cg.WaitContext(ctx); err != nil {
		return err
	}
	d.cg.Cancel()
	return nil
}

// ShutdownTimeout calls ShutdownContext with a timeout-bounded context.
func (d *Dispatcher) ShutdownTimeout(timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return d.ShutdownContext(ctx)
}

// Close cancels the worker context to abort in-flight handlers, then waits for
// all workers to exit. Use it to force termination after ShutdownContext times
// out.
func (d *Dispatcher) Close() {
	d.cg.Cancel()
	d.cg.Wait()
}

func (d *Dispatcher) worker() {
	defer d.cg.Done()
	ctx := d.cg.Context()
	for {
		select {
		case <-ctx.Done():
			// Forced termination (Close): stop even if eventCh is still open.
			return
		case item, ok := <-d.eventCh:
			if !ok {
				// Graceful drain complete: the producer closed the channel.
				return
			}
			if err := d.registry.Handle(ctx, item.event, item.client); err != nil {
				slog.ErrorContext(ctx, "github events poller: handler error",
					"deliveryID", item.event.GetDeliveryID(),
					"eventType", item.event.EventType(),
					"error", err,
				)
			}
		}
	}
}
