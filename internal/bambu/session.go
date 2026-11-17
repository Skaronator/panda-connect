package bambu

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/prusa3d/prusa-connect/sdk/connectorapi"
	"github.com/skaronator/panda-connect/internal/telemetry"
)

type identityAnswer struct {
	identity connectorapi.Identity
	model    modelProfile
	err      error
}

// Embed only the core SDK interfaces, not the optional H3C file interfaces.
type monitorSession struct {
	connectorapi.Session
	connectorapi.CountsLink
	connectorapi.ReportsProbe
}

// Session owns one authenticated MQTT connection and its telemetry stream.
type Session struct {
	client              mqtt.Client
	serial              string
	identity            connectorapi.Identity
	caps                connectorapi.Set
	timeout, staleAfter time.Duration
	log                 *slog.Logger
	incoming            chan receivedFrame
	state               chan connectorapi.StatePatch
	done, exited        chan struct{}
	stopOnce, closeOnce sync.Once
	identified          chan identityAnswer
	initialState        chan struct{}
	sequence            atomic.Uint64
	dropped, refused    atomic.Uint64
	refreshLock         chan struct{}
	reportMu            sync.Mutex
	reportSignal        chan struct{}
	fullReportAt        time.Time
	controlLock         chan struct{}
	controlMu           sync.Mutex
	pendingControl      *pendingControl
	inventoryRevision   uint64
	values              map[string]string
	storage             *storageAccess
	transferLock        chan struct{}
	storageSeen         atomic.Bool
	storageChanged      chan struct{}
	life                context.Context
	cancelLife          context.CancelFunc
	approvals           map[string]PrintApproval
}

var (
	_ connectorapi.Session      = (*Session)(nil)
	_ connectorapi.CountsLink   = (*Session)(nil)
	_ connectorapi.ReportsProbe = (*Session)(nil)
)

func newSession(timeout, stale time.Duration, serial string, caps connectorapi.Set, log *slog.Logger) *Session {
	life, cancel := context.WithCancel(context.Background())
	return &Session{
		serial: serial, caps: caps, timeout: timeout, staleAfter: stale, log: log,
		incoming: make(chan receivedFrame, 16), state: make(chan connectorapi.StatePatch),
		done: make(chan struct{}), exited: make(chan struct{}), identified: make(chan identityAnswer, 1),
		initialState: make(chan struct{}), reportSignal: make(chan struct{}), refreshLock: make(chan struct{}, 1),
		controlLock: make(chan struct{}, 1), values: make(map[string]string),
		transferLock: make(chan struct{}, 1), storageChanged: make(chan struct{}, 1), life: life, cancelLife: cancel,
	}
}

// Identity returns the identity established during the handshake.
func (s *Session) Identity() connectorapi.Identity { return s.identity }

// Capabilities returns a copy; this session has one immutable epoch.
func (s *Session) Capabilities() (connectorapi.Set, uint64) { return s.caps.Clone(), 1 }

// Probe tells the host what is actually available after resolving the printer's
// identity and endpoint opt-ins. This narrows the host's API and UI descriptors.
func (s *Session) Probe() connectorapi.Set { return s.caps.Clone() }

// State returns normalized state patches. Only run sends and closes this channel.
func (s *Session) State() <-chan connectorapi.StatePatch { return s.state }

// Dropped counts pending values replaced by newer values for the same path.
func (s *Session) Dropped() uint64 { return s.dropped.Load() }

// Refused counts malformed frames and frames lost to an overflowing input queue.
func (s *Session) Refused() uint64 { return s.refused.Load() }

func (s *Session) stop() { s.stopOnce.Do(func() { s.cancelLife(); close(s.done) }) }

// Close is idempotent and waits until the state writer exits.
func (s *Session) Close() error {
	s.stop()
	s.closeOnce.Do(func() { s.client.Disconnect(0) })
	<-s.exited
	return nil
}

// Do validates even direct SDK calls; no caller can bypass the safety bounds.
func (s *Session) Do(ctx context.Context, cmd connectorapi.Command) (connectorapi.Result, error) {
	if err := ctx.Err(); err != nil {
		return connectorapi.Result{}, err
	}
	checked, err := s.caps.Validate(cmd)
	if err != nil {
		return connectorapi.Result{}, err
	}
	if checked.Verb != VerbSendStateInfo {
		if checked.Verb == connectorapi.VerbSendInfo {
			err := s.AskStorages(ctx)
			return connectorapi.Result{OK: err == nil}, err
		}
		if checked.Verb == connectorapi.VerbSendFileInfo {
			_, err := s.List(ctx, checked.Args["path"])
			return connectorapi.Result{OK: err == nil}, err
		}
		return s.control(ctx, checked)
	}
	return s.refresh(ctx)
}

func (s *Session) refresh(ctx context.Context) (connectorapi.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	select {
	case s.refreshLock <- struct{}{}:
		defer func() { <-s.refreshLock }()
	case <-ctx.Done():
		return connectorapi.Result{}, ctx.Err()
	case <-s.done:
		return connectorapi.Result{}, RefreshFailed.New("printer disconnected")
	}
	requested := time.Now()
	if err := s.publish(ctx, "pushing", "pushall"); err != nil {
		return connectorapi.Result{}, RefreshFailed.New("cannot request a full status report").Wrap(err)
	}
	for {
		s.reportMu.Lock()
		next, latest := s.reportSignal, s.fullReportAt
		s.reportMu.Unlock()
		if !latest.Before(requested) {
			break
		}
		select {
		case <-ctx.Done():
			return connectorapi.Result{}, RefreshFailed.New("status refresh timed out").Wrap(ctx.Err())
		case <-s.done:
			return connectorapi.Result{}, RefreshFailed.New("printer disconnected")
		case <-next:
		}
	}
	if err := ctx.Err(); err != nil {
		return connectorapi.Result{}, err
	}
	select {
	case <-s.done:
		return connectorapi.Result{}, RefreshFailed.New("printer disconnected")
	default:
		return connectorapi.Result{OK: true}, nil
	}
}

func (s *Session) run() {
	defer close(s.exited)
	defer close(s.state)
	defer s.stop()
	stale := time.NewTimer(s.staleAfter)
	defer stale.Stop()
	var parse func([]byte) ([]connectorapi.StatePatch, bool, bool, error)
	var model modelProfile
	identified, initial := false, false
	// Each pending path has at most one value. A slow host never loses a field
	// that the printer reports only once, and memory is bounded by the fixed parser.
	pending := make(map[string]json.RawMessage)
	var order []string
	for {
		var output chan connectorapi.StatePatch
		var patch connectorapi.StatePatch
		if len(order) > 0 {
			output = s.state
			patch = connectorapi.StatePatch{Path: order[0], Value: pending[order[0]]}
		}
		select {
		case <-s.done:
			return
		case <-stale.C:
			s.log.Warn("Panda Connect telemetry timed out; ending session")
			return
		case output <- patch:
			delete(pending, patch.Path)
			order = order[1:]
		case received := <-s.incoming:
			frame := received.data
			if !json.Valid(frame) {
				s.refused.Add(1)
				return
			}
			if !identified {
				answer, found := decodeIdentity(frame, s.serial)
				if !found {
					continue
				}
				s.identified <- answer
				if answer.err != nil {
					return
				}
				model, identified = answer.model, true
				if model.rackInventory {
					parse = (&telemetry.H3C{}).Parse
				} else {
					parse = telemetry.NewStandard(model.monitor).Parse
				}
				continue
			}
			if model.controls || model.dispatch {
				s.controlReply(frame, received.at)
			}
			patches, full, status, err := parse(frame)
			if err != nil {
				s.refused.Add(1)
				// A rejected delta may have been a safety-relevant change.
				// Reconnect instead of dispatching against a stale projection.
				return
			}
			if !status {
				continue
			}
			if !initial && !full {
				continue
			}
			if model.controls || model.dispatch {
				s.observeControl(patches, received.at)
			}
			stale.Reset(s.staleAfter)
			if full {
				s.reportMu.Lock()
				s.fullReportAt = received.at
				close(s.reportSignal)
				s.reportSignal = make(chan struct{})
				s.reportMu.Unlock()
				if !initial {
					close(s.initialState)
					initial = true
				}
			}
			for _, next := range patches {
				if _, exists := pending[next.Path]; exists {
					s.dropped.Add(1)
				} else {
					order = append(order, next.Path)
				}
				pending[next.Path] = next.Value
			}
			// Coalescing must not move terminal state ahead of its outcome or
			// move a new job's readings ahead of its id.
			sort.SliceStable(order, func(i, j int) bool { return patchPriority(order[i]) < patchPriority(order[j]) })
		}
	}
}

func patchPriority(path string) int {
	switch path {
	case connectorapi.JobIDPath:
		return 0
	case connectorapi.JobOutcomePath:
		return 1
	case connectorapi.JobStatePath:
		return 3
	case "/state":
		return 4
	default:
		return 2
	}
}
