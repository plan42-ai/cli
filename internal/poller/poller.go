package poller

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/debugging-sucks/concurrency"
	"github.com/debugging-sucks/ecies"
	"github.com/debugging-sucks/event-horizon-sdk-go/eh"
	"github.com/debugging-sucks/event-horizon-sdk-go/eh/messages"
	"github.com/debugging-sucks/runner/internal/log"
	"github.com/debugging-sucks/runner/internal/util"
	"github.com/google/uuid"
)

const maxRetries = 5

type queueInfo struct {
	queueID    string
	ctx        context.Context
	cancel     context.CancelFunc
	drain      chan struct{}
	draining   bool
	privateKey *ecdsa.PrivateKey
}

type Poller struct {
	cg                     *concurrency.ContextGroup
	ctx                    context.Context
	queues                 []*queueInfo
	nExpectedQueueCount    int64
	nActualQueueCount      int64
	lastScaleEvent         time.Time
	sumBatchPct            float64
	nBatches               int64
	measureStart           time.Time
	scaleTicker            *time.Ticker
	scaleCtx               context.Context
	cancelScale            context.CancelFunc
	mux                    sync.Mutex
	client                 *eh.Client
	tenantID               string
	runnerID               string
	queueManagementBackoff *concurrency.Backoff
	batchBackoff           *concurrency.Backoff
}

func (p *Poller) scale() {
	defer p.cg.Done()
	defer p.cancelScale()
	defer p.scaleTicker.Stop()

	for {
		select {
		case <-p.scaleCtx.Done():
			return
		case <-p.scaleTicker.C:
		}

		p.doScale()
	}
}

func (p *Poller) doScale() {
	p.mux.Lock()
	defer p.mux.Unlock()
	now := time.Now()

	// We are still waiting for the last scale operation to complete, return.
	if p.nExpectedQueueCount != p.nActualQueueCount {
		return
	}

	// We don't have at least one minute of utilization data yet, return.
	if now.Sub(p.measureStart) < time.Minute {
		return
	}

	// If it's been less than one min since the last scale event, return.
	if now.Sub(p.lastScaleEvent) < time.Minute {
		return
	}

	// quick sanity check to avoid divide by 0.
	if p.nBatches == 0 {
		return
	}

	if p.sumBatchPct/float64(p.nBatches) >= 0.8 {
		// It's been at least 1 min since the last scale operation
		// and our average batch size is >= 80% full over at least 1 min. Double the number of queues.
		p.scaleUp()
		return
	}

	// We don't have at least 2 mins of measurement data, so we can't make any scale down decisions.
	// return.
	if now.Sub(p.measureStart) < time.Minute*2 {
		return
	}

	// We can only scale down every 2 mins, so if it's been less than 2 mins since the last scale event,
	// or we are still waiting on a scale down event, return.
	if now.Sub(p.lastScaleEvent) < time.Minute*2 {
		// reset our stats window
		p.resetStats()
		return
	}

	if p.sumBatchPct/float64(p.nBatches) <= 0.4 {
		// It's been at least 2 mins since the last scale operation
		// and our average batch size is <= 40% full over at least 2 mins.
		// Decrease the number of queues by 1.
		p.scaleDown()
		return
	}

	// The average batch has been > 40% full and < 80% full for the last 2 mins.
	// So, we are in a "good" steady state. No need to scale anything. Just
	// reset our stat window.
	p.resetStats()
}

func (p *Poller) resetStats() {
	p.measureStart = time.Now()
	p.nBatches = 0
	p.sumBatchPct = 0.0
}

func createQueueInfo(ctx context.Context) *queueInfo {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		slog.ErrorContext(ctx, "ecdss.GenerateKey failed", "error", err)
		return nil
	}
	qi := &queueInfo{
		queueID:    uuid.NewString(),
		ctx:        nil,
		cancel:     nil,
		drain:      make(chan struct{}),
		privateKey: key,
	}
	qi.ctx, qi.cancel = context.WithCancel(ctx)
	qi.ctx = log.WithContextAttrs(qi.ctx, slog.String("queueID", qi.queueID))
	return qi
}

func (p *Poller) scaleUp() {
	p.resetStats()

	nToAdd := len(p.queues)
	for i := 0; i < nToAdd; i++ {
		qi := createQueueInfo(p.cg.Context())
		if qi == nil {
			continue
		}
		p.nExpectedQueueCount++
		p.queues = append(p.queues, qi)
		p.cg.Add(1)
		go p.poll(qi)
	}

	if p.nExpectedQueueCount == p.nActualQueueCount {
		p.lastScaleEvent = time.Now()
	}
}

func (p *Poller) scaleDown() {
	p.resetStats()
	if len(p.queues) == 1 {
		p.lastScaleEvent = time.Now()
		return
	}
	p.nExpectedQueueCount--
	last := p.queues[len(p.queues)-1]
	p.queues[len(p.queues)-1] = nil
	p.queues = p.queues[:len(p.queues)-1]
	p.signalDrain(last)
}

func (p *Poller) drainAll() {
	p.mux.Lock()
	defer p.mux.Unlock()
	p.resetStats()
	p.nExpectedQueueCount = 0
	for _, qi := range p.queues {
		p.signalDrain(qi)
	}
	p.queues = nil
}

func (p *Poller) poll(qi *queueInfo) {
	defer p.cg.Done()
	defer qi.cancel()

	err := p.createQueue(qi)
	defer p.decreaseActualQueueCount()
	if err != nil {
		return
	}
	defer p.deleteQueue(qi)

	req := eh.GetMessagesBatchRequest{
		TenantID: p.tenantID,
		RunnerID: p.runnerID,
		QueueID:  qi.queueID,
	}
loop:
	for {
		select {
		case <-qi.ctx.Done():
			return
		case <-qi.drain:
			break loop
		default:
		}
		p.doPoll(qi, &req)
	}

	p.markAsDraining(qi)
	p.signalDrain(qi)

	startDrain := time.Now()
	for {
		select {
		case <-qi.ctx.Done():
			return
		default:
		}
		n := p.doPoll(qi, &req)
		if n == 0 && time.Since(startDrain) >= 30*time.Second {
			return
		}
	}
}

func (p *Poller) doPoll(qi *queueInfo, req *eh.GetMessagesBatchRequest) int {
	err := p.batchBackoff.WaitContext(qi.ctx)
	if err != nil {
		return 0
	}

	batch, err := p.client.GetMessagesBatch(qi.ctx, req)
	if err != nil {
		slog.ErrorContext(p.ctx, "unable to get messages batch", "error", err)
		p.batchBackoff.Backoff()
		return 0
	}

	if len(batch.Messages) == 0 {
		p.batchBackoff.Backoff()
	} else {
		p.batchBackoff.Recover()
	}

	p.addStats(float64(len(batch.Messages)) / 10.0)
	for _, msg := range batch.Messages {
		p.cg.Add(1)
		go p.processMessage(msg, qi)
	}
	return len(batch.Messages)
}

func (p *Poller) decreaseActualQueueCount() {
	p.mux.Lock()
	defer p.mux.Unlock()
	p.nActualQueueCount--
	if p.nActualQueueCount == p.nExpectedQueueCount {
		p.lastScaleEvent = time.Now()
	}
}

func (p *Poller) increaseActualQueueCount() {
	p.mux.Lock()
	defer p.mux.Unlock()
	p.nActualQueueCount++
	if p.nActualQueueCount == p.nExpectedQueueCount {
		p.lastScaleEvent = time.Now()
	}
}

func (p *Poller) createQueue(qi *queueInfo) error {
	defer p.increaseActualQueueCount()

	for {
		select {
		case <-qi.ctx.Done():
			return qi.ctx.Err()
		case <-qi.drain:
			return errors.New("draining queue before create succeeded")
		default:
		}

		err := p.queueManagementBackoff.WaitContext(qi.ctx)
		if err != nil {
			return err
		}

		pubPem, err := ecies.PubKeyToPem(&qi.privateKey.PublicKey)

		if err != nil {
			panic(err)
		}

		_, err = p.client.RegisterRunnerQueue(
			qi.ctx,
			&eh.RegisterRunnerQueueRequest{
				TenantID:  p.tenantID,
				RunnerID:  p.runnerID,
				QueueID:   qi.queueID,
				PublicKey: pubPem,
			},
		)

		var conflictErr *eh.ConflictError
		if errors.As(err, &conflictErr) {
			// if we get a conflict error, the queue already exists. return
			return nil
		}

		if err != nil {
			p.queueManagementBackoff.Backoff()
			slog.ErrorContext(p.ctx, "RegisterRunnerQueue failed", "error", err)
			continue
		}
		slog.InfoContext(qi.ctx, "successfully created queue")
		p.queueManagementBackoff.Recover()
		return nil
	}
}

func (p *Poller) addStats(pct float64) {
	p.mux.Lock()
	defer p.mux.Unlock()
	p.sumBatchPct += pct
	p.nBatches++
}

func (p *Poller) processMessage(msg *eh.RunnerMessage, qi *queueInfo) {
	defer p.cg.Done()
	ctx := log.WithContextAttrs(
		qi.ctx,
		slog.String("messageID", msg.MessageID),
		slog.String("callerID", msg.CallerID),
	)
	callerPub, err := ecies.PemToPubKey(msg.CallerPublicKey)
	if err != nil {
		slog.ErrorContext(ctx, "unable to parse caller public key", "error", err)
		return
	}

	decrypted, err := ecies.Unwrap(msg.Payload.(*ecies.WrappedSecret), qi.privateKey)
	if err != nil {
		slog.ErrorContext(ctx, "unable to decrypt ECIES message", "error", err)
		return
	}
	parsedMsg, err := parseMessage(decrypted)
	if err != nil {
		slog.ErrorContext(ctx, "unable to parse message", "error", err)
		return
	}
	resp := parsedMsg.Process(ctx)
	respJSON, err := json.Marshal(resp)
	if err != nil {
		slog.ErrorContext(ctx, "unable to marshal response", "error", err)
		return
	}

	encryptedResp, err := ecies.Wrap(respJSON, callerPub.(*ecdsa.PublicKey))
	if err != nil {
		slog.ErrorContext(ctx, "unable to encrypt response", "error", err)
		return
	}

	err = p.client.WriteResponse(
		qi.ctx,
		&eh.WriteResponseRequest{
			TenantID:  p.tenantID,
			RunnerID:  p.runnerID,
			QueueID:   qi.queueID,
			MessageID: msg.MessageID,
			CallerID:  msg.CallerID,
			Payload:   encryptedResp,
		},
	)

	if err != nil {
		slog.ErrorContext(ctx, "unable to write response", "error", err)
	}
}

func parseMessage(data []byte) (pollerMessage, error) {
	var tmp struct {
		Type messages.MessageType `json:"type"`
	}
	err := json.Unmarshal(data, &tmp)
	if err != nil {
		return nil, err
	}
	var target pollerMessage
	switch tmp.Type {
	case messages.PingRequestMessage:
		target = &pollerPingRequest{}
	default:
		return nil, fmt.Errorf("unknown message type: %v", tmp.Type)
	}
	err = json.Unmarshal(data, target)
	if err != nil {
		return nil, err
	}
	return target, nil
}

func (p *Poller) ShutdownContext(ctx context.Context) error {
	p.drainAll()
	p.cancelScale()
	return p.cg.WaitContext(ctx)
}

func (p *Poller) ShutdownTimeout(timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return p.ShutdownContext(ctx)
}

func (p *Poller) Close() error {
	return p.cg.Close()
}

func (p *Poller) deleteQueue(qi *queueInfo) {
	var queue *eh.RunnerQueue
	var err error

	for i := 0; i < maxRetries; i++ {
		err = p.queueManagementBackoff.WaitContext(qi.ctx)
		if err != nil {
			slog.ErrorContext(qi.ctx, "Unable to delete queue: backoff wait failed", "error", err)
			return
		}

		if queue == nil {
			queue, err = p.client.GetRunnerQueue(
				qi.ctx,
				&eh.GetRunnerQueueRequest{
					TenantID: p.tenantID,
					RunnerID: p.runnerID,
					QueueID:  qi.queueID,
				},
			)

			if err != nil {
				slog.ErrorContext(qi.ctx, "Unable to delete queue: GetRunnerQueue failed", "error", err)
				p.queueManagementBackoff.Backoff()
				continue
			}
		}

		err = p.client.DeleteRunnerQueue(
			qi.ctx,
			&eh.DeleteRunnerQueueRequest{
				TenantID: p.tenantID,
				RunnerID: p.runnerID,
				QueueID:  qi.queueID,
				Version:  queue.Version,
			},
		)

		var conflictErr *eh.ConflictError
		if errors.As(err, &conflictErr) {
			queue, _ = conflictErr.Current.(*eh.RunnerQueue)
		}

		if err != nil {
			slog.ErrorContext(qi.ctx, "Unable to delete queue: DeleteRunnerQueue failed", "error", err)
			p.queueManagementBackoff.Backoff()
			continue
		}
		slog.InfoContext(qi.ctx, "Deleted queue")
		p.queueManagementBackoff.Recover()
		return
	}
	slog.ErrorContext(qi.ctx, "Unable to delete queue: exhausted retries", "error", err)
}

func (p *Poller) markAsDraining(qi *queueInfo) {
	var queue *eh.RunnerQueue
	var err error

	for i := 0; i < maxRetries; i++ {
		err = p.queueManagementBackoff.WaitContext(qi.ctx)
		if err != nil {
			slog.ErrorContext(qi.ctx, "Unable to mark queue as draining: backoff wait failed", "error", err)
			return
		}

		if queue == nil {
			queue, err = p.client.GetRunnerQueue(
				qi.ctx,
				&eh.GetRunnerQueueRequest{
					TenantID: p.tenantID,
					RunnerID: p.runnerID,
					QueueID:  qi.queueID,
				},
			)

			if err != nil {
				slog.ErrorContext(qi.ctx, "Unable to mark queue as draining: GetRunnerQueue failed", "error", err)
				p.queueManagementBackoff.Backoff()
				continue
			}
		}

		_, err = p.client.UpdateRunnerQueue(
			qi.ctx,
			&eh.UpdateRunnerQueueRequest{
				TenantID:  p.tenantID,
				RunnerID:  p.runnerID,
				QueueID:   qi.queueID,
				Version:   queue.Version,
				Draining:  util.Pointer(true),
				IsHealthy: util.Pointer(false),
			},
		)

		var conflictErr *eh.ConflictError
		if errors.As(err, &conflictErr) {
			queue, _ = conflictErr.Current.(*eh.RunnerQueue)
		}

		if err != nil {
			slog.ErrorContext(qi.ctx, "Unable to mark queue as draining: UpdateRunnerQueue failed", "error", err)
			p.queueManagementBackoff.Backoff()
			continue
		}
		p.queueManagementBackoff.Recover()
		slog.InfoContext(qi.ctx, "Marked queue as draining", "queue", qi.queueID)
		return
	}
	slog.ErrorContext(qi.ctx, "Unable to mark queue as drained: exhausted retries", "error", err)
}

func (p *Poller) signalDrain(qi *queueInfo) {
	if !qi.draining {
		close(qi.drain)
		qi.draining = true
	}
}

func New(client *eh.Client, tenantID string, runnerID string) *Poller {
	cg := concurrency.NewContextGroup()
	ctx := log.WithContextAttrs(
		cg.Context(),
		slog.String("tenantID", tenantID),
		slog.String("runnerID", runnerID),
	)
	qi := createQueueInfo(ctx)
	if qi == nil {
		panic("failed to create queue info")
	}

	scaleTicker := time.NewTicker(1 * time.Second)
	scaleCtx, cancelScale := context.WithCancel(ctx)

	ret := &Poller{
		cg:  cg,
		ctx: ctx,
		queues: []*queueInfo{
			qi,
		},
		nExpectedQueueCount:    1,
		nActualQueueCount:      0,
		sumBatchPct:            0,
		nBatches:               0,
		measureStart:           time.Now(),
		scaleTicker:            scaleTicker,
		scaleCtx:               scaleCtx,
		cancelScale:            cancelScale,
		client:                 client,
		tenantID:               tenantID,
		runnerID:               runnerID,
		queueManagementBackoff: concurrency.NewBackoff(10*time.Millisecond, 5*time.Second),
		batchBackoff:           concurrency.NewBackoff(1*time.Millisecond, 50*time.Millisecond),
	}
	ret.cg.Add(2)
	go ret.scale()
	go ret.poll(qi)
	return ret
}
