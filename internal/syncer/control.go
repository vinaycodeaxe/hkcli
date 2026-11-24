package syncer

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vinaycodeaxe/hkcli/internal/api"
	"github.com/vinaycodeaxe/hkcli/internal/mqtt"
	"github.com/vinaycodeaxe/hkcli/internal/protocol"
	"github.com/vinaycodeaxe/hkcli/internal/store"
)

// Hike's relay handles a second web connection of the same account badly: it was seen closing
// the first one, or keeping it open while it stopped delivering to it. So a store has at most one
// relay connection. Whoever holds the store's relay lock (store.RelayLockPath) owns it: a running
// `sync` while it connects, listens and disconnects (it lets go while it waits to reconnect), or
// another command while it sends on a short-lived connection of its own. OpenMQTT takes a
// RelayLease, so no code path can connect without owning the relay. Lock order: the store's LOCK,
// then the relay lock, then the token refresh lock.
//
// Commands hand their packets to a `sync` that owns the relay through a local endpoint: `sync`
// listens on 127.0.0.1 and records the port and a random secret in the store (store.ControlPath).
// Both sides prove they know the secret (HMAC over fresh nonces) before anything else is sent, so
// a stale record whose port another program took over learns nothing.
//
// Time budget (the OpenClaw channel's limits in brackets): a command gives up waiting for an
// owner after publishBudget - controlHandoffTimeout and then still allows one hand-off, so it
// finishes within publishBudget (75 s) [send timeout 90 s]. A hand-off carries its remaining
// budget; the sync waits at most controlConnectWait for a reconnecting relay and publishes within
// that budget, so it never outlives the command. On closing a connection, the sync waits up to
// controlDrainTimeout (25 s) for hand-offs, then closes it within controlTeardownTimeout (5 s);
// on stop, the endpoint then lets requests still being read finish (controlRequestTimeout, 10 s):
// 40 s in all [stop grace 45 s].

const (
	controlProtocol        = 2
	controlDialTimeout     = 2 * time.Second
	controlRequestTimeout  = 10 * time.Second // each request line must arrive within this
	controlConnectWait     = 10 * time.Second // a hand-off waits this long for a reconnecting relay
	controlPublishTimeout  = 30 * time.Second // publish budget for clients that send none (hkcli 0.4.0)
	controlHandoffTimeout  = 50 * time.Second // a command waits this long for the sync's answer
	controlStatusTimeout   = 5 * time.Second
	controlDrainTimeout    = 25 * time.Second // a closing connection waits this long for hand-offs
	controlTeardownTimeout = 5 * time.Second  // then closes within this
	controlRetryInterval   = 250 * time.Millisecond
	controlNonceBytes      = 16
	maxControlRequest      = 8 << 20
	maxControlResponse     = 64 << 10
)

// publishBudget bounds a command's whole publish, including waiting for a sync that is
// starting, reconnecting or stopping (a variable so tests can shorten it).
var publishBudget = 75 * time.Second

// How a command's packet went out (the "via" field of send and mark-read output).
const (
	ViaSync = "sync" // through the running sync's relay connection
	ViaOwn  = "own"  // on a short-lived connection of this command's own
)

// RelayUnavailableError means nothing was sent: another hkcli process owns this store's relay
// connection and did not take the packet in time.
type RelayUnavailableError struct{ Msg string }

func (e *RelayUnavailableError) Error() string { return e.Msg }

// TooLargeError means a packet is too large to hand to the running sync; nothing was sent.
type TooLargeError struct{ Size int }

func (e *TooLargeError) Error() string {
	return fmt.Sprintf("the message is too large to hand to the running hkcli sync (%d bytes); nothing was sent", e.Size)
}

var errNotOwner = errors.New("the recorded endpoint did not prove that it belongs to the running hkcli sync")

// RelayLease proves its holder owns the store's relay connection: it holds the relay lock.
type RelayLease struct{ lock *store.FileLock }

// AcquireRelayLease takes the store's relay lock, retrying for up to timeout (0 tries once). It
// returns nil and no error when another process keeps holding it.
func AcquireRelayLease(ctx context.Context, st *store.Store, timeout time.Duration) (*RelayLease, error) {
	lock := store.NewFileLock(st.RelayLockPath())
	ok, err := lock.Acquire(ctx, timeout)
	if err != nil || !ok {
		return nil, err
	}
	return &RelayLease{lock: lock}, nil
}

// Release gives the relay up; close every connection opened under the lease first. It is safe
// to call more than once.
func (l *RelayLease) Release() {
	if l.held() {
		l.lock.Release()
		l.lock = nil
	}
}

func (l *RelayLease) held() bool { return l != nil && l.lock != nil }

// RelayOwnerPID reads the pid that the relay lock's holder wrote (0 when unknown).
func RelayOwnerPID(st *store.Store) int {
	return store.NewFileLock(st.RelayLockPath()).HolderPID()
}

type controlEndpoint struct {
	V     int    `json:"v,omitempty"` // controlProtocol; absent in hkcli 0.4.0
	PID   int    `json:"pid"`
	Port  int    `json:"port"`
	Token string `json:"token"`
}

type controlRequest struct {
	Op       string `json:"op"` // "hello", then "publish" or "status"
	Nonce    string `json:"nonce,omitempty"`
	Auth     string `json:"auth,omitempty"`
	Token    string `json:"token,omitempty"` // hkcli 0.4.0 commands send the secret itself
	Payload  string `json:"payload,omitempty"`
	QoS      byte   `json:"qos,omitempty"`
	BudgetMS int64  `json:"budget_ms,omitempty"` // how long the command still waits for the answer
}

type controlHello struct {
	V     int    `json:"v"`
	Proof string `json:"proof"`
	Nonce string `json:"nonce"`
}

type controlResponse struct {
	OK        bool   `json:"ok"`
	ServerID  string `json:"server_id,omitempty"`
	Connected bool   `json:"connected,omitempty"`
	// Sent is false when nothing reached the relay.
	Sent bool `json:"sent"`
	// Stopping: try again shortly. The sync is reconnecting or stopping and its relay connection
	// may still be open, so the command must not connect on its own meanwhile.
	Stopping bool   `json:"stopping,omitempty"`
	Error    string `json:"error,omitempty"`
}

// controlHooks is what the endpoint needs from the sync (tests pass fakes).
type controlHooks struct {
	publish   func(ctx context.Context, payload []byte, qos byte, deadline time.Time) controlResponse
	connected func() bool
	// stopping reports that the sync is closing its relay connection for good.
	stopping func() bool
}

// ServeControl lets other hkcli commands on this store publish over the sync's relay
// connection. Once ctx ends or Follow is closing its relay connection for good, it answers
// that the sync is stopping, until stop is called after the relay connection closed; stop
// removes the endpoint recorded in path.
func (s *Syncer) ServeControl(ctx context.Context, path string) (stop func(), err error) {
	return serveControl(ctx, path, controlHooks{publish: s.controlPublish, connected: s.relayConnected, stopping: s.isStopping})
}

func serveControl(ctx context.Context, path string, hooks controlHooks) (func(), error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	token, err := randomHex(32)
	if err != nil {
		ln.Close()
		return nil, err
	}
	endpoint := controlEndpoint{V: controlProtocol, PID: os.Getpid(), Port: ln.Addr().(*net.TCPAddr).Port, Token: token}
	data, err := json.Marshal(endpoint)
	if err != nil {
		ln.Close()
		return nil, err
	}
	if err := store.WriteFileAtomic(path, string(data)); err != nil {
		ln.Close()
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	var handlers sync.WaitGroup
	handlers.Add(1)
	go func() {
		defer handlers.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			handlers.Add(1)
			go func() {
				defer handlers.Done()
				serveControlConn(ctx, conn, token, hooks)
			}()
		}
	}()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			// Commands waiting on a stopping sync connect on their own once its relay lock is free.
			ln.Close()
			if current, ok := readControlEndpoint(path); ok && current.Token == token {
				_ = os.Remove(path)
			}
			cancel()
			handlers.Wait()
		})
	}
	return stop, nil
}

func serveControlConn(ctx context.Context, conn net.Conn, token string, hooks controlHooks) {
	defer conn.Close()
	reader := bufio.NewReader(io.LimitReader(conn, maxControlRequest+maxControlResponse))
	// Commands write each line at once; a silent connection must not hold up a stopping sync.
	_ = conn.SetDeadline(time.Now().Add(controlRequestTimeout))
	var req controlRequest
	if readControlLine(reader, &req) != nil {
		return
	}
	authorized := false
	switch {
	case req.Op == "hello":
		nonce, err := randomHex(controlNonceBytes)
		if err != nil || req.Nonce == "" || len(req.Nonce) > 4*controlNonceBytes {
			return
		}
		if writeControlLine(conn, controlHello{V: controlProtocol, Proof: controlProof(token, "sync", req.Nonce), Nonce: nonce}) != nil {
			return
		}
		_ = conn.SetDeadline(time.Now().Add(controlRequestTimeout))
		req = controlRequest{}
		if readControlLine(reader, &req) != nil {
			return
		}
		authorized = hmac.Equal([]byte(req.Auth), []byte(controlProof(token, "command", nonce)))
	case req.Token != "":
		authorized = subtle.ConstantTimeCompare([]byte(req.Token), []byte(token)) == 1 // hkcli 0.4.0
	}
	budget := controlPublishTimeout
	if req.BudgetMS > 0 {
		budget = min(time.Duration(req.BudgetMS)*time.Millisecond, controlHandoffTimeout)
	}
	deadline := time.Now().Add(budget)
	_ = conn.SetDeadline(deadline.Add(controlTeardownTimeout))
	var resp controlResponse
	switch {
	case !authorized:
		resp.Error = "not authorized"
	case ctx.Err() != nil || hooks.stopping():
		resp.Error, resp.Stopping = "sync is stopping", true
	case req.Op == "status":
		resp.OK, resp.Connected = true, hooks.connected()
	case req.Op == "publish":
		resp = hooks.publish(ctx, []byte(req.Payload), req.QoS, deadline)
	default:
		resp.Error = "unknown operation " + strconv.Quote(req.Op)
	}
	_ = writeControlLine(conn, resp)
}

// controlProof shows knowledge of the endpoint secret without revealing it.
func controlProof(token, role, nonce string) string {
	mac := hmac.New(sha256.New, []byte(token))
	mac.Write([]byte("hkcli sync control " + role + " " + nonce))
	return hex.EncodeToString(mac.Sum(nil))
}

func (s *Syncer) setClient(client *mqtt.Client) {
	s.clientMu.Lock()
	s.client = client
	s.clientMu.Unlock()
}

func (s *Syncer) liveClient() *mqtt.Client {
	s.clientMu.Lock()
	defer s.clientMu.Unlock()
	return s.client
}

func (s *Syncer) relayConnected() bool {
	client := s.liveClient()
	return client != nil && client.Connected()
}

// detachClient keeps control requests off the relay connection Follow is closing. final marks
// the sync as stopping for good.
func (s *Syncer) detachClient(final bool) {
	s.clientMu.Lock()
	s.client = nil
	s.stopping = s.stopping || final
	s.clientMu.Unlock()
}

func (s *Syncer) isStopping() bool {
	s.clientMu.Lock()
	defer s.clientMu.Unlock()
	return s.stopping
}

func (s *Syncer) setOwnsRelay(owns bool) {
	s.clientMu.Lock()
	s.ownsRelay = owns
	s.clientMu.Unlock()
}

// takeRelay waits until this sync holds the store's relay lock. A command holds it while it
// sends on a short-lived connection of its own.
func (s *Syncer) takeRelay(ctx context.Context, st *store.Store) (*RelayLease, error) {
	told := false
	for {
		lease, err := AcquireRelayLease(ctx, st, time.Second)
		if err != nil {
			return nil, err
		}
		if lease != nil {
			s.setOwnsRelay(true)
			return lease, nil
		}
		if !told {
			told = true
			holder := ""
			if pid := RelayOwnerPID(st); pid > 0 {
				holder = fmt.Sprintf(" (pid %d)", pid)
			}
			s.out.Status(fmt.Sprintf("waiting for another hkcli process%s to close its relay connection", holder))
		}
	}
}

// releaseRelay lets commands connect on their own until this sync takes the relay again.
func (s *Syncer) releaseRelay(lease *RelayLease) {
	s.setOwnsRelay(false)
	lease.Release()
}

// closeRelay closes a connection Follow detached. Hand-offs on it get up to controlDrainTimeout
// to finish (the ones still running then fail as "may have been sent"), and the goodbye gets
// controlTeardownTimeout, so the relay lock is let go of in bounded time.
func (s *Syncer) closeRelay(client *mqtt.Client) {
	if !s.drainPublishes(controlDrainTimeout) {
		client.Close()
	}
	watchdog := time.AfterFunc(controlTeardownTimeout, client.Close)
	defer watchdog.Stop()
	if client.Connected() {
		_, _ = PublishPacket(context.Background(), client, s.me, protocol.BuildPresence(s.me, false), 0)
	}
	client.Disconnect()
}

// acquireClient returns the connected relay client and counts a publish on it, so Follow can
// let that publish finish before it closes the connection (see drainPublishes). Without one,
// connecting reports whether this sync holds the relay and so will have a connection soon.
func (s *Syncer) acquireClient() (client *mqtt.Client, stopping, connecting bool) {
	s.clientMu.Lock()
	defer s.clientMu.Unlock()
	if s.stopping {
		return nil, true, false
	}
	if s.client == nil || !s.client.Connected() {
		return nil, false, s.ownsRelay
	}
	s.publishing++
	return s.client, false, true
}

func (s *Syncer) releaseClient() {
	s.clientMu.Lock()
	s.publishing--
	s.clientMu.Unlock()
}

// drainPublishes waits up to timeout for publishes other commands started on the connection
// that is being closed, and reports whether they all finished. Call it after detachClient, so no
// new ones start.
func (s *Syncer) drainPublishes(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		s.clientMu.Lock()
		busy := s.publishing > 0
		s.clientMu.Unlock()
		if !busy {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// controlPublish publishes for another command before deadline, waiting briefly for a relay
// that is reconnecting. Once the publish started, a failure means the message may have been
// sent. The publish is not tied to ctx: a sync that is stopping still finishes it.
func (s *Syncer) controlPublish(ctx context.Context, payload []byte, qos byte, deadline time.Time) controlResponse {
	stopping := controlResponse{Error: "sync is stopping", Stopping: true}
	// "Stopping" also keeps hkcli 0.4.0 commands from connecting on their own meanwhile.
	reconnecting := controlResponse{Error: "sync is reconnecting to Hike", Stopping: true}
	waitUntil := time.Now().Add(controlConnectWait)
	if deadline.Before(waitUntil) {
		waitUntil = deadline
	}
	for {
		if ctx.Err() != nil {
			return stopping
		}
		client, closing, connecting := s.acquireClient()
		switch {
		case closing:
			return stopping
		case client != nil:
			publishCtx, cancel := context.WithDeadline(context.WithoutCancel(ctx), deadline)
			serverID, err := publishPayload(publishCtx, client, s.me, payload, qos)
			cancel()
			s.releaseClient()
			if err != nil {
				return controlResponse{Sent: true, Error: err.Error()}
			}
			return controlResponse{OK: true, Sent: true, ServerID: serverID}
		case !connecting || !time.Now().Before(waitUntil):
			// Without the relay lock this sync has no connection to wait for: the command may
			// take the lock and connect on its own.
			return reconnecting
		}
		if wait(ctx, 100*time.Millisecond) != nil {
			return stopping
		}
	}
}

// PublishOnStore publishes payload (a packet as compact JSON, QoS 1) for a command on store st,
// over the store's one relay connection, and reports which way it went (ViaSync or ViaOwn).
// While another process owns the relay, the packet goes through it if it is a running sync; this
// command never connects meanwhile. Otherwise it takes the relay lock and publishes on a
// short-lived connection of its own, using the API client from connect.
func PublishOnStore(ctx context.Context, st *store.Store, connect func() (*api.Client, error), payload []byte) (serverID, via string, err error) {
	start := time.Now()
	retryUntil := start.Add(publishBudget - controlHandoffTimeout)
	req := controlRequest{Op: "publish", Payload: string(payload), QoS: 1, Auth: strings.Repeat("0", 2*sha256.Size)}
	fits := encodedSize(req) <= maxControlRequest
	for {
		lease, err := AcquireRelayLease(ctx, st, 0)
		if err != nil {
			return "", "", err
		}
		if lease != nil {
			defer lease.Release()
			serverID, err := publishOnOwnConnection(ctx, connect, lease, payload)
			return serverID, ViaOwn, err
		}
		if !fits {
			return "", "", &TooLargeError{Size: len(payload)}
		}
		budget := max(time.Until(start.Add(publishBudget)), controlDialTimeout+controlRequestTimeout)
		serverID, done, err := publishThroughOwner(ctx, st.ControlPath(), RelayOwnerPID(st), req, budget)
		if done {
			return serverID, ViaSync, err
		}
		if !time.Now().Before(retryUntil) {
			return "", "", &RelayUnavailableError{Msg: "another hkcli process owns this store's relay connection and did not take the message in time; nothing was sent"}
		}
		if err := wait(ctx, controlRetryInterval); err != nil {
			return "", "", err
		}
	}
}

func publishOnOwnConnection(ctx context.Context, connect func() (*api.Client, error), lease *RelayLease, payload []byte) (string, error) {
	client, err := connect()
	if err != nil {
		return "", err
	}
	relay, err := OpenMQTT(ctx, client, lease)
	if err != nil {
		return "", err
	}
	defer relay.Disconnect()
	return publishPayload(ctx, relay, client.Session.UserID, payload, 1)
}

// publishThroughOwner hands req to the sync recorded at path when that sync holds the relay lock
// as pid owner. done is false when nothing was sent and the caller should try again: the owner
// has no endpoint yet (or is another command), or it is reconnecting or stopping.
func publishThroughOwner(ctx context.Context, path string, owner int, req controlRequest, budget time.Duration) (serverID string, done bool, err error) {
	endpoint, ok := readControlEndpoint(path)
	if !ok || endpoint.V < controlProtocol || owner <= 0 || endpoint.PID != owner {
		return "", false, nil
	}
	resp, sent, err := callControl(ctx, endpoint, req, budget)
	switch {
	case ctx.Err() != nil:
		return "", true, ctx.Err()
	case err != nil && !sent:
		return "", false, nil
	case err != nil:
		return "", true, &mqtt.Error{Kind: mqtt.KindTimeout, Msg: fmt.Sprintf("the running hkcli sync did not answer (%v); the message may or may not have been sent", err)}
	case resp.OK:
		return resp.ServerID, true, nil
	case resp.Sent:
		return "", true, &mqtt.Error{Kind: mqtt.KindError, Msg: resp.Error}
	}
	return "", false, nil
}

// SyncRelayStatus asks the sync recorded at path, which holds the relay lock as pid owner,
// whether its relay connection is up. ok is false when it did not answer.
func SyncRelayStatus(ctx context.Context, path string, owner int) (connected, ok bool) {
	endpoint, found := readControlEndpoint(path)
	if !found || endpoint.V < controlProtocol || owner <= 0 || endpoint.PID != owner {
		return false, false
	}
	resp, _, err := callControl(ctx, endpoint, controlRequest{Op: "status"}, controlStatusTimeout)
	if err != nil || !resp.OK {
		return false, false
	}
	return resp.Connected, true
}

// callControl authenticates both sides, then sends one request. sent reports whether the request
// reached the sync: until then nothing of it was disclosed or acted on.
func callControl(ctx context.Context, endpoint controlEndpoint, req controlRequest, budget time.Duration) (resp controlResponse, sent bool, err error) {
	dialer := net.Dialer{Timeout: controlDialTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(endpoint.Port)))
	if err != nil {
		return resp, false, err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	reader := bufio.NewReader(io.LimitReader(conn, maxControlResponse))
	_ = conn.SetDeadline(time.Now().Add(controlRequestTimeout))
	nonce, err := randomHex(controlNonceBytes)
	if err != nil {
		return resp, false, err
	}
	if err := writeControlLine(conn, controlRequest{Op: "hello", Nonce: nonce}); err != nil {
		return resp, false, err
	}
	var hello controlHello
	if err := readControlLine(reader, &hello); err != nil {
		return resp, false, err
	}
	if !hmac.Equal([]byte(hello.Proof), []byte(controlProof(endpoint.Token, "sync", nonce))) {
		return resp, false, errNotOwner
	}
	req.Auth = controlProof(endpoint.Token, "command", hello.Nonce)
	req.Token = ""
	if req.Op == "publish" {
		req.BudgetMS = (budget - controlTeardownTimeout).Milliseconds()
	}
	_ = conn.SetDeadline(time.Now().Add(budget))
	// The sync acts only on a complete line, so a failed write sent nothing.
	if err := writeControlLine(conn, req); err != nil {
		return resp, false, err
	}
	if err := readControlLine(reader, &resp); err != nil {
		return resp, true, err
	}
	return resp, true, nil
}

func readControlLine(r *bufio.Reader, v any) error {
	line, err := r.ReadBytes('\n')
	if err != nil {
		return err
	}
	return json.Unmarshal(line, v)
}

func writeControlLine(w io.Writer, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = w.Write(append(data, '\n'))
	return err
}

func encodedSize(v any) int {
	data, err := json.Marshal(v)
	if err != nil {
		return maxControlRequest + 1
	}
	return len(data) + 1
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func readControlEndpoint(path string) (controlEndpoint, bool) {
	var endpoint controlEndpoint
	data, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(data, &endpoint) != nil {
		return endpoint, false
	}
	return endpoint, endpoint.Port > 0 && endpoint.Port <= 65535 && endpoint.Token != ""
}
