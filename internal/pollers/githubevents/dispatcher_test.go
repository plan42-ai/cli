package githubevents

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	ghapi "github.com/plan42-ai/github-event-handlers/github"
	"github.com/plan42-ai/github-event-handlers/handlers"
	"github.com/stretchr/testify/require"
)

func TestDispatcherHandlesAllEvents(t *testing.T) {
	registry := handlers.NewHandlerRegistry(handlers.Config{})

	var handleCount atomic.Int64
	registry.Register(testEventType, func(_ context.Context, _ handlers.Event, _ ghapi.API) {
		handleCount.Add(1)
	})

	eventCh := make(chan handlers.Event, 10)
	d := NewDispatcher(registry, eventCh, 3)

	for range 5 {
		eventCh <- &testEvent{evtType: testEventType, delivery: "d1"}
	}
	close(eventCh)

	// Shut down and wait for workers to drain.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, d.ShutdownContext(ctx))

	require.Equal(t, int64(5), handleCount.Load(), "all 5 events should have been handled")
}

func TestDispatcherDrainsBeforeShutdownReturns(t *testing.T) {
	var mu sync.Mutex
	var events []string

	registry := handlers.NewHandlerRegistry(handlers.Config{})
	registry.Register(testEventType, func(_ context.Context, evt handlers.Event, _ ghapi.API) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, evt.GetDeliveryID())
	})

	eventCh := make(chan handlers.Event, 10)
	d := NewDispatcher(registry, eventCh, 2)

	for i := range 5 {
		eventCh <- &testEvent{evtType: testEventType, delivery: string(rune('A' + i))}
	}
	close(eventCh)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, d.ShutdownContext(ctx))

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, events, 5, "all events should be drained by workers before shutdown completes")
}

func TestDispatcherCloseForcesTermination(t *testing.T) {
	registry := handlers.NewHandlerRegistry(handlers.Config{})
	registry.Register(testEventType, func(ctx context.Context, _ handlers.Event, _ ghapi.API) {
		<-ctx.Done() // block until the worker context is cancelled
	})

	eventCh := make(chan handlers.Event, 10)
	d := NewDispatcher(registry, eventCh, 1)

	// Note: eventCh is left open, the abort/shutdown condition where workers
	// must observe cancellation rather than waiting for the channel to close.
	eventCh <- &testEvent{evtType: testEventType, delivery: "d1"}

	// The handler blocks on ctx.Done(), so a graceful drain can't complete and
	// ShutdownContext times out without cancelling.
	err := d.ShutdownTimeout(50 * time.Millisecond)
	require.ErrorIs(t, err, context.DeadlineExceeded)

	// Close cancels the worker context, aborting the handler, then waits for the
	// workers to exit. It must return rather than hang even with eventCh open.
	done := make(chan struct{})
	go func() {
		d.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not return")
	}
}
