package syncer

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/vinaycodeaxe/hkcli/internal/mqtt"
	"github.com/vinaycodeaxe/hkcli/internal/store"
)

// Hike's relay handles a second web connection of the same account badly: it may close the
// first one, or keep it open and quietly stop delivering to it. So while `sync` follows the
// relay, other commands on the same store publish through its connection. `sync` listens on
// 127.0.0.1 and records the port and a random token in the store (store.ControlPath).

type controlEndpoint struct {
	PID   int    `json:"pid"`
	Port  int    `json:"port"`
	Token string `json:"token"`
}

type controlRequest struct {
	Token   string `json:"token"`
	Op      string `json:"op"` // "publish" or "status"
	Payload string `json:"payload,omitempty"`
	QoS     byte   `json:"qos,omitempty"`
}

type controlResponse struct {
	OK        bool   `json:"ok"`
	ServerID  string `json:"server_id,omitempty"`
	Connected bool   `json:"connected,omitempty"`
	// Sent is false when nothing reached the relay, so the caller can still publish itself.
	Sent  bool   `json:"sent"`
	Error string `json:"error,omitempty"`
}

const (
	controlDialTimeout    = 2 * time.Second
	controlRequestTimeout = 10 * time.Second
	// A publish waits up to controlConnectWait for a reconnecting relay, then takes up to
	// ~25 s (three tries); the reply deadline covers both.
	controlConnectWait    = 10 * time.Second
	controlPublishTimeout = 60 * time.Second
	controlStatusTimeout  = 5 * time.Second
	// How long a closing connection waits for publishes other commands started on it.
	controlDrainTimeout = 25 * time.Second
	maxControlRequest   = 8 << 20
)

type controlPublisher func(ctx context.Context, payload []byte, qos byte) controlResponse

// ServeControl lets other hkcli commands on this store publish over the sync's relay
// connection until ctx ends or stop is called. It records the endpoint in path and removes it
// again on stop.
func (s *Syncer) ServeControl(ctx context.Context, path string) (stop func(), err error) {
	return serveControl(ctx, path, s.controlPublish, s.relayConnected)
}

func serveControl(ctx context.Context, path string, publish controlPublisher, connected func() bool) (func(), error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		ln.Close()
		return nil, err
	}
	endpoint := controlEndpoint{PID: os.Getpid(), Port: ln.Addr().(*net.TCPAddr).Port, Token: hex.EncodeToString(secret)}
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
				serveControlConn(ctx, conn, endpoint.Token, publish, connected)
			}()
		}
	}()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			// New requests fail fast and fall back to their own connection from here on.
			ln.Close()
			if current, ok := readControlEndpoint(path); ok && current.Token == endpoint.Token {
				_ = os.Remove(path)
			}
			cancel()
			handlers.Wait()
		})
	}
	context.AfterFunc(ctx, func() { go stop() })
	return stop, nil
}

func serveControlConn(ctx context.Context, conn net.Conn, token string, publish controlPublisher, connected func() bool) {
	defer conn.Close()
	// Clients write their request at once; a silent one must not hold up a stopping sync.
	_ = conn.SetDeadline(time.Now().Add(controlRequestTimeout))
	line, err := bufio.NewReader(io.LimitReader(conn, maxControlRequest)).ReadBytes('\n')
	if err != nil {
		return
	}
	_ = conn.SetDeadline(time.Now().Add(controlPublishTimeout))
	var req controlRequest
	var resp controlResponse
	switch {
	case json.Unmarshal(line, &req) != nil:
		resp.Error = "malformed request"
	case subtle.ConstantTimeCompare([]byte(req.Token), []byte(token)) != 1:
		resp.Error = "wrong control token"
	case req.Op == "status":
		resp.OK, resp.Connected = true, connected()
	case req.Op == "publish" && ctx.Err() == nil:
		resp = publish(ctx, []byte(req.Payload), req.QoS)
	case req.Op == "publish":
		resp.Error = "sync is stopping"
	default:
		resp.Error = "unknown operation " + strconv.Quote(req.Op)
	}
	data, err := json.Marshal(resp)
	if err == nil {
		_, _ = conn.Write(append(data, '\n'))
	}
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

// acquireClient returns the connected relay client and counts a publish on it, so Follow can
// let that publish finish before it closes the connection (see drainPublishes).
func (s *Syncer) acquireClient() *mqtt.Client {
	s.clientMu.Lock()
	defer s.clientMu.Unlock()
	if s.client == nil || !s.client.Connected() {
		return nil
	}
	s.publishing++
	return s.client
}

func (s *Syncer) releaseClient() {
	s.clientMu.Lock()
	s.publishing--
	s.clientMu.Unlock()
}

// drainPublishes waits up to timeout for publishes other commands started on the connection
// that is being closed. Call it after setClient(nil), so no new ones start.
func (s *Syncer) drainPublishes(timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for {
		s.clientMu.Lock()
		busy := s.publishing > 0
		s.clientMu.Unlock()
		if !busy || !time.Now().Before(deadline) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// controlPublish publishes for another command, waiting briefly for a relay that is
// reconnecting. Once the publish started, a failure means the message may have been sent. The
// publish is not tied to ctx: a sync that is stopping still finishes it (drainPublishes).
func (s *Syncer) controlPublish(ctx context.Context, payload []byte, qos byte) controlResponse {
	deadline := time.Now().Add(controlConnectWait)
	for {
		if client := s.acquireClient(); client != nil {
			publishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), controlPublishTimeout)
			serverID, err := publishPayload(publishCtx, client, s.me, payload, qos)
			cancel()
			s.releaseClient()
			if err != nil {
				return controlResponse{Sent: true, Error: err.Error()}
			}
			return controlResponse{OK: true, Sent: true, ServerID: serverID}
		}
		if !time.Now().Before(deadline) || wait(ctx, 100*time.Millisecond) != nil {
			return controlResponse{Error: "sync is not connected to Hike right now"}
		}
	}
}

// PublishViaSync hands payload to a `sync` running on the same store, whose control endpoint
// is recorded at path. handled is false when no sync took it and nothing was sent: the caller
// then publishes over a connection of its own.
func PublishViaSync(ctx context.Context, path string, payload []byte, qos byte) (serverID string, handled bool, err error) {
	endpoint, ok := readControlEndpoint(path)
	if !ok {
		return "", false, nil
	}
	resp, sent, err := callControl(ctx, endpoint, controlRequest{Op: "publish", Payload: string(payload), QoS: qos}, controlPublishTimeout)
	switch {
	case err != nil && !sent:
		return "", false, nil
	case err != nil:
		if ctx.Err() != nil {
			return "", true, ctx.Err()
		}
		return "", true, &mqtt.Error{Kind: mqtt.KindTimeout, Msg: fmt.Sprintf("the running hkcli sync did not answer (%v); the message may or may not have been sent", err)}
	case !resp.OK && !resp.Sent:
		return "", false, nil
	case !resp.OK:
		return "", true, &mqtt.Error{Kind: mqtt.KindError, Msg: resp.Error}
	}
	return resp.ServerID, true, nil
}

// SyncRelayStatus asks a `sync` running on the same store whether its relay connection is up.
// ok is false when no sync answered.
func SyncRelayStatus(ctx context.Context, path string) (connected, ok bool, pid int) {
	endpoint, found := readControlEndpoint(path)
	if !found {
		return false, false, 0
	}
	resp, _, err := callControl(ctx, endpoint, controlRequest{Op: "status"}, controlStatusTimeout)
	if err != nil || !resp.OK {
		return false, false, 0
	}
	return resp.Connected, true, endpoint.PID
}

// callControl sends one request. sent reports whether the request reached the sync, so a
// failure before that point is safe to retry another way.
func callControl(ctx context.Context, endpoint controlEndpoint, req controlRequest, timeout time.Duration) (resp controlResponse, sent bool, err error) {
	dialer := net.Dialer{Timeout: controlDialTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(endpoint.Port)))
	if err != nil {
		return resp, false, err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	req.Token = endpoint.Token
	data, err := json.Marshal(req)
	if err != nil {
		return resp, false, err
	}
	// The sync acts only on a complete line, so a failed write sent nothing.
	if _, err := conn.Write(append(data, '\n')); err != nil {
		return resp, false, err
	}
	line, err := bufio.NewReader(io.LimitReader(conn, maxControlRequest)).ReadBytes('\n')
	if err != nil {
		return resp, true, err
	}
	if err := json.Unmarshal(line, &resp); err != nil {
		return resp, true, fmt.Errorf("malformed answer: %w", err)
	}
	return resp, true, nil
}

func readControlEndpoint(path string) (controlEndpoint, bool) {
	var endpoint controlEndpoint
	data, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(data, &endpoint) != nil {
		return endpoint, false
	}
	return endpoint, endpoint.Port > 0 && endpoint.Port <= 65535 && endpoint.Token != ""
}
