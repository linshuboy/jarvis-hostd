package hostdrun

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"agi/runtime/hostd/internal/appctl"
	"agi/runtime/hostd/internal/buildinfo"
	"agi/runtime/hostd/internal/config"
	"agi/runtime/hostd/internal/host"
	"agi/runtime/hostd/internal/protocol"
	"agi/runtime/hostd/internal/state"
	"agi/runtime/hostd/internal/wsclient"
)

var errPairingRequired = errors.New("runtime pairing required")
var errReconnectRequested = errors.New("runtime reconnect requested")

const (
	maxHeartbeatProbeInterval = 5 * time.Second
	minHeartbeatAckTimeout    = 3 * time.Second
	maxHeartbeatAckTimeout    = 10 * time.Second
)

type Options struct {
	Config        config.Config
	StateStore    *state.Store
	HostComponent *host.Component
	Dialer        wsclient.Dialer
	Logger        *log.Logger
}

type Runner struct {
	config        config.Config
	stateStore    *state.Store
	hostComponent *host.Component
	dialer        wsclient.Dialer
	logger        *log.Logger
	now           func() time.Time
	id            func() string
	reconnectCh   chan struct{}
	statusMu      sync.RWMutex
	online        bool
	stateName     string
	controlPath   string
	runCancelMu   sync.Mutex
	runCancel     context.CancelFunc
}

func New(options Options) *Runner {
	logger := options.Logger
	if logger == nil {
		logger = log.New(log.Writer(), "hostd ", log.LstdFlags|log.Lmsgprefix)
	}
	return &Runner{
		config:        options.Config,
		stateStore:    options.StateStore,
		hostComponent: options.HostComponent,
		dialer:        options.Dialer,
		logger:        logger,
		now:           func() time.Time { return time.Now().UTC() },
		id:            func() string { return fmt.Sprintf("%d", time.Now().UTC().UnixNano()) },
		reconnectCh:   make(chan struct{}, 1),
		stateName:     "starting",
	}
}

func (r *Runner) Run(ctx context.Context) error {
	runCtx, cancel := context.WithCancel(ctx)
	r.setRunCancel(cancel)
	defer r.setRunCancel(nil)
	if r.stateStore == nil {
		return fmt.Errorf("state store is required")
	}
	if strings.TrimSpace(r.config.Gateway.WSURL) == "" {
		return fmt.Errorf("gateway.ws_url is required")
	}
	if r.dialer == nil {
		r.dialer = wsclient.DefaultDialer{}
	}
	if err := r.startControlServer(runCtx); err != nil {
		r.logger.Printf("app control server unavailable: %v", err)
	}
	backoff := time.Second
	for {
		r.setConnectionState("connecting", false)
		err := r.runOnce(runCtx)
		if err == nil || errors.Is(err, context.Canceled) {
			return err
		}
		if runCtx.Err() != nil {
			return runCtx.Err()
		}
		if errors.Is(err, errReconnectRequested) {
			backoff = time.Second
			continue
		}
		if errors.Is(err, errPairingRequired) {
			r.setConnectionState("waiting_for_pairing", false)
			r.logger.Printf("pairing required; runtime will retry with persisted runtime_id")
		} else {
			r.setConnectionState("backoff", false)
			r.logger.Printf("connection loop ended: %v", err)
		}
		select {
		case <-runCtx.Done():
			return runCtx.Err()
		case <-r.reconnectCh:
			backoff = time.Second
			continue
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
		}
	}
}

func (r *Runner) runOnce(ctx context.Context) error {
	current, err := r.stateStore.Update(func(value *state.State) error {
		_, ensureErr := state.EnsureRuntimeID(value)
		return ensureErr
	})
	if err != nil {
		return err
	}
	conn, err := r.dialer.Dial(ctx, wsclient.Options{
		URL:            r.config.Gateway.WSURL,
		TLSMode:        r.config.Gateway.TLSMode,
		TLSFingerprint: r.config.Gateway.TLSFingerprint,
	})
	if err != nil {
		_, _ = r.stateStore.Update(func(value *state.State) error {
			value.LastError = err.Error()
			return nil
		})
		r.setConnectionState("backoff", false)
		return err
	}
	defer conn.Close()
	r.logger.Printf("connecting to %s as runtime %s", r.config.Gateway.WSURL, current.RuntimeID)
	connectID := r.id()
	if err := conn.WriteJSON(ctx, r.buildConnectRequest(current, connectID)); err != nil {
		return err
	}
	var response protocol.RequestFrame
	if err := conn.ReadJSON(ctx, &response); err != nil {
		return err
	}
	if !response.OK {
		errorCode := ""
		errorMessage := "connect rejected"
		if response.Error != nil {
			errorCode = strings.TrimSpace(response.Error.Code)
			errorMessage = strings.TrimSpace(response.Error.Message)
		}
		_, _ = r.stateStore.Update(func(value *state.State) error {
			value.LastError = fmt.Sprintf("%s: %s", errorCode, errorMessage)
			if errorCode == "PAIRING_REQUIRED" {
				value.PairingState = state.PairingStatePending
			}
			return nil
		})
		if errorCode == "PAIRING_REQUIRED" {
			return errPairingRequired
		}
		r.setConnectionState("backoff", false)
		return fmt.Errorf("connect rejected: %s", errorMessage)
	}
	hello, err := protocol.ParseHelloPayload(response)
	if err != nil {
		return err
	}
	current, err = r.stateStore.Update(func(value *state.State) error {
		value.PairingState = hello.Runtime.PairingState
		value.LastGatewayURL = r.config.Gateway.WSURL
		value.LastConnectedAt = r.now().Format(time.RFC3339)
		value.LastError = ""
		return nil
	})
	if err != nil {
		return err
	}
	r.setConnectionState("connected", true)
	accepted := acceptedComponents(hello.Runtime.AcceptedComponents)
	if len(accepted) > 0 && r.config.Components.Host.Enabled && r.hostComponent != nil && !accepted[r.hostComponent.Definition().ComponentID] {
		r.logger.Printf("server did not accept host.main component")
	}
	interval := time.Duration(r.config.HeartbeatSeconds) * time.Second
	if hello.Policy.TickIntervalMS > 0 {
		interval = time.Duration(hello.Policy.TickIntervalMS) * time.Millisecond
	}
	probeInterval := heartbeatProbeInterval(interval)
	ackTimeout := heartbeatAckTimeout(probeInterval)
	readFrames := make(chan protocol.RequestFrame)
	readErrors := make(chan error, 1)
	go func() {
		for {
			var frame protocol.RequestFrame
			if err := conn.ReadJSON(ctx, &frame); err != nil {
				readErrors <- err
				return
			}
			readFrames <- frame
		}
	}()
	ticker := time.NewTicker(probeInterval)
	defer ticker.Stop()
	pongTimer := time.NewTimer(ackTimeout)
	stopTimer(pongTimer)
	awaitingPong := false
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-r.reconnectCh:
			r.setConnectionState("connecting", false)
			return errReconnectRequested
		case <-ticker.C:
			if awaitingPong {
				continue
			}
			if err := conn.WriteJSON(ctx, r.buildHeartbeat(current, accepted)); err != nil {
				_, _ = r.stateStore.Update(func(value *state.State) error {
					value.LastError = err.Error()
					return nil
				})
				r.setConnectionState("backoff", false)
				return err
			}
			awaitingPong = true
			resetTimer(pongTimer, ackTimeout)
		case <-pongTimer.C:
			if !awaitingPong {
				continue
			}
			timeoutErr := fmt.Errorf("heartbeat acknowledgement timeout after %s", ackTimeout.Round(time.Second))
			_, _ = r.stateStore.Update(func(value *state.State) error {
				value.LastError = timeoutErr.Error()
				return nil
			})
			r.setConnectionState("backoff", false)
			return timeoutErr
		case err := <-readErrors:
			_, _ = r.stateStore.Update(func(value *state.State) error {
				value.LastError = err.Error()
				return nil
			})
			r.setConnectionState("backoff", false)
			return err
		case frame := <-readFrames:
			if awaitingPong {
				awaitingPong = false
				stopTimer(pongTimer)
			}
			switch frame.Type {
			case "event":
				continue
			case "req":
				if err := r.handleRequest(ctx, conn, frame); err != nil {
					r.logger.Printf("handle request %s %s failed: %v", frame.ID, frame.Method, err)
				}
			}
		}
	}
}

func (r *Runner) buildConnectRequest(current state.State, requestID string) protocol.ConnectRequest {
	return protocol.ConnectRequest{
		Type:   "req",
		ID:     requestID,
		Method: "connect",
		Params: protocol.ConnectParams{
			MinProtocol: 1,
			MaxProtocol: 1,
			Client: protocol.ClientDescriptor{
				ID:       "hostd",
				Version:  r.runtimeVersion(),
				Platform: runtime.GOOS,
				Mode:     "node",
			},
			Role: "node",
			Auth: protocol.AuthPayload{
				Token: current.RuntimeToken,
			},
			Runtime:    r.buildRuntimeDescriptor(current),
			Components: r.buildComponents(nil),
		},
	}
}

func (r *Runner) buildRuntimeDescriptor(current state.State) protocol.RuntimeDescriptor {
	return protocol.RuntimeDescriptor{
		ID:          current.RuntimeID,
		DisplayName: r.config.DisplayName,
		Hostname:    r.hostName(),
		Platform:    runtime.GOOS,
		Version:     r.runtimeVersion(),
		Metadata: map[string]any{
			"arch": runtime.GOARCH,
		},
	}
}

func (r *Runner) buildHeartbeat(current state.State, accepted map[string]bool) protocol.EventFrame {
	return protocol.EventFrame{
		Type:  "event",
		Event: "node.heartbeat",
		Payload: map[string]any{
			"runtimeId":  current.RuntimeID,
			"ttlSeconds": maxInt(5, r.config.HeartbeatSeconds*3),
			"runtime": map[string]any{
				"displayName": r.config.DisplayName,
				"metadata": map[string]any{
					"arch": runtime.GOARCH,
				},
			},
			"components": r.buildComponents(accepted),
		},
	}
}

func (r *Runner) buildComponents(accepted map[string]bool) []protocol.RuntimeComponent {
	if !r.config.Components.Host.Enabled || r.hostComponent == nil {
		return nil
	}
	component := r.hostComponent.Definition()
	if len(accepted) > 0 && !accepted[component.ComponentID] {
		return nil
	}
	return []protocol.RuntimeComponent{component}
}

func (r *Runner) handleRequest(ctx context.Context, conn wsclient.Conn, frame protocol.RequestFrame) error {
	if r.hostComponent == nil {
		return conn.WriteJSON(ctx, protocol.ErrorResponse{
			Type: "res",
			ID:   frame.ID,
			OK:   false,
			Error: protocol.ErrorPayload{
				Code:    "HOST_COMPONENT_DISABLED",
				Message: "host.main is disabled",
			},
		})
	}
	result, dispatchErr := r.hostComponent.Dispatch(frame.Method, frame.Params)
	if dispatchErr != nil {
		r.logger.Printf("dispatch request id=%s method=%s error=%s", frame.ID, frame.Method, dispatchErr.Code)
		return conn.WriteJSON(ctx, protocol.ErrorResponse{
			Type: "res",
			ID:   frame.ID,
			OK:   false,
			Error: protocol.ErrorPayload{
				Code:    dispatchErr.Code,
				Message: dispatchErr.Message,
				Details: dispatchErr.Details,
			},
		})
	}
	r.logger.Printf("dispatch request id=%s method=%s ok", frame.ID, frame.Method)
	return conn.WriteJSON(ctx, protocol.SuccessResponse{
		Type:    "res",
		ID:      frame.ID,
		OK:      true,
		Payload: result,
	})
}

func acceptedComponents(values []string) map[string]bool {
	if len(values) == 0 {
		return nil
	}
	result := make(map[string]bool, len(values))
	for _, item := range values {
		item = strings.TrimSpace(item)
		if item != "" {
			result[item] = true
		}
	}
	return result
}

func stringValue(value any) string {
	item, _ := value.(string)
	return item
}

func hostName(value any) string {
	item, _ := value.(string)
	return item
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (r *Runner) runtimeVersion() string {
	if r.hostComponent == nil {
		return buildinfo.RuntimeVersion()
	}
	return stringValue(r.hostComponent.Metadata()["runtime_version"])
}

func (r *Runner) hostName() string {
	if r.hostComponent == nil {
		return ""
	}
	return hostName(r.hostComponent.Metadata()["hostname"])
}

func (r *Runner) startControlServer(ctx context.Context) error {
	if r.stateStore == nil {
		return nil
	}
	controlSocketPath, err := config.ResolveControlSocketPath(config.Options{
		StatePath: r.stateStore.Path(),
	})
	if err != nil {
		return err
	}
	server, err := appctl.Start(ctx, appctl.ServerOptions{
		SocketPath:        controlSocketPath,
		Snapshot:          r.snapshotStatus,
		SetRuntimeToken:   r.setRuntimeToken,
		ClearRuntimeToken: r.clearRuntimeToken,
		RequestReconnect:  r.requestReconnectSnapshot,
		Shutdown:          r.requestShutdown,
	})
	if err != nil {
		return err
	}
	r.controlPath = controlSocketPath
	go func() {
		<-ctx.Done()
		_ = server.Close()
	}()
	return nil
}

func (r *Runner) snapshotStatus() (appctl.Snapshot, error) {
	current, err := r.stateStore.Load()
	if err != nil {
		return appctl.Snapshot{}, err
	}
	r.statusMu.RLock()
	defer r.statusMu.RUnlock()
	return appctl.Snapshot{
		RuntimeID:         current.RuntimeID,
		PairingState:      current.PairingState,
		HasRuntimeToken:   strings.TrimSpace(current.RuntimeToken) != "",
		LastGatewayURL:    current.LastGatewayURL,
		LastConnectedAt:   current.LastConnectedAt,
		LastError:         current.LastError,
		Online:            r.online,
		ConnectionState:   r.stateName,
		HelperPID:         os.Getpid(),
		ControlSocketPath: r.controlPath,
	}, nil
}

func heartbeatProbeInterval(interval time.Duration) time.Duration {
	if interval <= 0 {
		return time.Second
	}
	if interval > maxHeartbeatProbeInterval {
		return maxHeartbeatProbeInterval
	}
	return interval
}

func heartbeatAckTimeout(probeInterval time.Duration) time.Duration {
	if probeInterval <= 0 {
		probeInterval = time.Second
	}
	timeout := probeInterval + (probeInterval / 2)
	if timeout < minHeartbeatAckTimeout {
		timeout = minHeartbeatAckTimeout
	}
	if timeout > maxHeartbeatAckTimeout {
		timeout = maxHeartbeatAckTimeout
	}
	return timeout
}

func stopTimer(timer *time.Timer) {
	if timer == nil {
		return
	}
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}

func resetTimer(timer *time.Timer, duration time.Duration) {
	if timer == nil {
		return
	}
	stopTimer(timer)
	timer.Reset(duration)
}

func (r *Runner) setRuntimeToken(token string) (appctl.Snapshot, error) {
	trimmed := strings.TrimSpace(token)
	if trimmed == "" {
		return appctl.Snapshot{}, fmt.Errorf("runtime token is required")
	}
	if _, err := r.stateStore.Update(func(value *state.State) error {
		if _, ensureErr := state.EnsureRuntimeID(value); ensureErr != nil {
			return ensureErr
		}
		value.RuntimeToken = trimmed
		value.PairingState = state.PairingStatePaired
		value.LastError = ""
		return nil
	}); err != nil {
		return appctl.Snapshot{}, err
	}
	r.requestReconnect()
	return r.snapshotStatus()
}

func (r *Runner) clearRuntimeToken() (appctl.Snapshot, error) {
	if _, err := r.stateStore.Update(func(value *state.State) error {
		if _, ensureErr := state.EnsureRuntimeID(value); ensureErr != nil {
			return ensureErr
		}
		value.RuntimeToken = ""
		value.PairingState = state.PairingStateUnpaired
		value.LastError = ""
		return nil
	}); err != nil {
		return appctl.Snapshot{}, err
	}
	r.requestReconnect()
	return r.snapshotStatus()
}

func (r *Runner) requestReconnectSnapshot() (appctl.Snapshot, error) {
	r.requestReconnect()
	return r.snapshotStatus()
}

func (r *Runner) requestReconnect() {
	select {
	case r.reconnectCh <- struct{}{}:
	default:
	}
}

func (r *Runner) requestShutdown() error {
	r.setConnectionState("stopping", false)
	r.runCancelMu.Lock()
	cancel := r.runCancel
	r.runCancelMu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil
}

func (r *Runner) setRunCancel(cancel context.CancelFunc) {
	r.runCancelMu.Lock()
	defer r.runCancelMu.Unlock()
	r.runCancel = cancel
}

func (r *Runner) setConnectionState(stateName string, online bool) {
	r.statusMu.Lock()
	defer r.statusMu.Unlock()
	r.stateName = strings.TrimSpace(stateName)
	r.online = online
}
