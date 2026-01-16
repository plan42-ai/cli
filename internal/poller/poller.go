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
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/google/go-github/v81/github"
	"github.com/google/uuid"
	"github.com/plan42-ai/cli/internal/config"
	"github.com/plan42-ai/cli/internal/util"
	"github.com/plan42-ai/concurrency"
	"github.com/plan42-ai/ecies"
	"github.com/plan42-ai/log"
	"github.com/plan42-ai/sdk-go/p42"
	"github.com/plan42-ai/sdk-go/p42/messages"
)

const maxRetries = 5

type queueInfo struct {
	queueID    string
	ctx        context.Context
	cancel     context.CancelFunc
	drain      chan struct{}
	draining   bool
	skipDelete bool
	privateKey *ecdsa.PrivateKey
}

type Option func(p *Poller)

type Poller struct {
	PlatformFields
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
	client                 *p42.Client
	tenantID               string
	runnerID               string
	queueManagementBackoff *concurrency.Backoff
	batchBackoff           *concurrency.Backoff
	connectionIdx          map[string]*config.GithubInfo
	githubClients          map[string]*github.Client
	githubClientMu         sync.Mutex
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
	defer p.deleteQueueIfNeeded(qi)

	req := p42.GetMessagesBatchRequest{
		TenantID:       p.tenantID,
		RunnerID:       p.runnerID,
		QueueID:        qi.queueID,
		MaxWaitSeconds: util.Pointer(30),
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
		_, stop := p.doPoll(qi, &req)
		if stop {
			return
		}
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
		n, stop := p.doPoll(qi, &req)
		if stop {
			return
		}
		if n == 0 && time.Since(startDrain) >= 30*time.Second {
			return
		}
	}
}

func (p *Poller) doPoll(qi *queueInfo, req *p42.GetMessagesBatchRequest) (n int, stop bool) {
	err := p.batchBackoff.WaitContext(qi.ctx)
	if err != nil {
		stop = true
		return
	}

	batch, err := p.client.GetMessagesBatch(qi.ctx, req)
	if err != nil {
		var httpErr p42.HTTPError
		if errors.As(err, &httpErr) && httpErr.Code() == http.StatusNotFound {
			p.handleQueueNotFound(qi)
			stop = true
			return
		}
		slog.ErrorContext(p.ctx, "unable to get messages batch", "error", err)
		p.batchBackoff.Backoff()
		return
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
	n = len(batch.Messages)
	return
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
			&p42.RegisterRunnerQueueRequest{
				TenantID:  p.tenantID,
				RunnerID:  p.runnerID,
				QueueID:   qi.queueID,
				PublicKey: pubPem,
			},
		)

		var conflictErr *p42.ConflictError
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

func (p *Poller) handleQueueNotFound(qi *queueInfo) {
	qi.skipDelete = true

	p.mux.Lock()
	defer p.mux.Unlock()

	if qi.draining || qi.ctx.Err() != nil || p.nExpectedQueueCount == 0 {
		slog.InfoContext(qi.ctx, "queue removed during shutdown; skipping replacement", "queue", qi.queueID)
		return
	}

	idx := slices.Index(p.queues, qi)

	if idx == -1 {
		slog.WarnContext(qi.ctx, "unable to replace missing queue", "queue", qi.queueID)
		return
	}

	p.nExpectedQueueCount--
	p.queues = append(p.queues[:idx], p.queues[idx+1:]...)

	replacement := createQueueInfo(p.cg.Context())
	if replacement == nil {
		slog.ErrorContext(qi.ctx, "unable to create replacement queue")
		return
	}

	p.nExpectedQueueCount++
	p.queues = append(p.queues, replacement)
	p.cg.Add(1)
	go p.poll(replacement)
	slog.InfoContext(qi.ctx, "replaced missing queue", "oldQueue", qi.queueID, "newQueue", replacement.queueID)
}

func (p *Poller) processMessage(msg *p42.RunnerMessage, qi *queueInfo) {
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
	parsedMsg, err := p.parseMessage(decrypted)
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
		&p42.WriteResponseRequest{
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

func (p *Poller) parseMessage(data []byte) (pollerMessage, error) {
	var tmp struct {
		Type messages.MessageType
	}

	err := json.Unmarshal(data, &tmp)
	if err != nil {
		return nil, err
	}

	var target pollerMessage

	switch tmp.Type {
	case messages.PingRequestMessage:
		target = &pollerPingRequest{}
	case messages.InvokeAgentRequestMessage:
		target = &pollerInvokeAgentRequest{}
	case messages.ListOrgsForGithubConnectionRequestMessage:
		target = &pollerListOrgsForGithubConnectionRequest{}
	case messages.SearchRepoRequestMessage:
		target = &pollerSearchRepoRequest{}
	default:
		return nil, fmt.Errorf("unknown message type: %v", tmp.Type)
	}

	err = json.Unmarshal(data, target)
	if err != nil {
		return nil, err
	}
	target.Init(p)
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

func (p *Poller) deleteQueueIfNeeded(qi *queueInfo) {
	if qi.skipDelete {
		return
	}
	p.deleteQueue(qi)
}

func (p *Poller) deleteQueue(qi *queueInfo) {
	var queue *p42.RunnerQueue
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
				&p42.GetRunnerQueueRequest{
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
			&p42.DeleteRunnerQueueRequest{
				TenantID: p.tenantID,
				RunnerID: p.runnerID,
				QueueID:  qi.queueID,
				Version:  queue.Version,
			},
		)

		var conflictErr *p42.ConflictError
		if errors.As(err, &conflictErr) {
			queue, _ = conflictErr.Current.(*p42.RunnerQueue)
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
	var queue *p42.RunnerQueue
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
				&p42.GetRunnerQueueRequest{
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
			&p42.UpdateRunnerQueueRequest{
				TenantID:  p.tenantID,
				RunnerID:  p.runnerID,
				QueueID:   qi.queueID,
				Version:   queue.Version,
				Draining:  util.Pointer(true),
				IsHealthy: util.Pointer(false),
			},
		)

		var conflictErr *p42.ConflictError
		if errors.As(err, &conflictErr) {
			queue, _ = conflictErr.Current.(*p42.RunnerQueue)
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

func New(client *p42.Client, tenantID string, runnerID string, options ...Option) *Poller {
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
		githubClients:          make(map[string]*github.Client),
	}
	for _, opt := range options {
		opt(ret)
	}
	ret.cg.Add(2)
	go ret.scale()
	go ret.poll(qi)
	return ret
}

func WithConnectionIdx(idx map[string]*config.GithubInfo) Option {
	return func(p *Poller) {
		p.connectionIdx = idx
	}
}

func (p *Poller) GetClientForConnectionID(connectionID string) (*github.Client, error) {
	p.githubClientMu.Lock()
	defer p.githubClientMu.Unlock()

	client := p.githubClients[connectionID]
	if client != nil {
		return client, nil
	}
	if p.connectionIdx == nil {
		return nil, fmt.Errorf("github connection index not configured")
	}
	cnn := p.connectionIdx[connectionID]
	if cnn == nil {
		return nil, fmt.Errorf("github connection %s not found", connectionID)
	}
	if cnn.Token == "" {
		return nil, fmt.Errorf("missing github token for connection %s", connectionID)
	}
	client = github.NewClient(nil).WithAuthToken(cnn.Token)
	if cnn.URL != "" && cnn.URL != defaultGithubURL {
		configured, err := client.WithEnterpriseURLs(cnn.URL, cnn.URL)
		if err != nil {
			return nil, fmt.Errorf("unable to configure github client: %w", err)
		}
		client = configured
	}
	p.githubClients[connectionID] = client
	return client, nil
}
