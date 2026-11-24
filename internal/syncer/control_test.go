package syncer

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vinaycodeaxe/hkcli/internal/api"
	"github.com/vinaycodeaxe/hkcli/internal/mqtt"
	"github.com/vinaycodeaxe/hkcli/internal/store"
)

var errOwnConnection = errors.New("connected on its own")

// ownConnection stands in for the API client a command connects with: it checks that the
// command holds the relay lock by then, and fails, so no network is needed.
func ownConnection(t *testing.T, st *store.Store, connected *atomic.Bool) func() (*api.Client, error) {
	return func() (*api.Client, error) {
		if lease, _ := AcquireRelayLease(context.Background(), st, 0); lease != nil {
			lease.Release()
			t.Error("the command connected without holding the relay lock")
		}
		connected.Store(true)
		return nil, errOwnConnection
	}
}

func noOwnConnection(t *testing.T) func() (*api.Client, error) {
	return func() (*api.Client, error) {
		t.Error("the command connected on its own while another process had the relay")
		return nil, errOwnConnection
	}
}

// startSync serves a control endpoint for st while holding st's relay lock, like a sync that has
// the relay connection. stop lets go of the relay and removes the endpoint.
func startSync(t *testing.T, ctx context.Context, st *store.Store, hooks controlHooks) (stop func()) {
	t.Helper()
	if hooks.connected == nil {
		hooks.connected = func() bool { return true }
	}
	if hooks.stopping == nil {
		hooks.stopping = func() bool { return false }
	}
	lease, err := AcquireRelayLease(context.Background(), st, 0)
	if err != nil || lease == nil {
		t.Fatalf("taking the relay lock: %v", err)
	}
	stopControl, err := serveControl(ctx, st.ControlPath(), hooks)
	if err != nil {
		lease.Release()
		t.Fatal(err)
	}
	var once sync.Once
	stop = func() {
		once.Do(func() {
			lease.Release()
			stopControl()
		})
	}
	t.Cleanup(stop)
	return stop
}

// shortBudget makes commands give up waiting for the relay after wait.
func shortBudget(t *testing.T, wait time.Duration) {
	saved := publishBudget
	publishBudget = controlHandoffTimeout + wait
	t.Cleanup(func() { publishBudget = saved })
}

func takeRelay(t *testing.T, st *store.Store) *RelayLease {
	t.Helper()
	lease, err := AcquireRelayLease(context.Background(), st, 0)
	if lease == nil || err != nil {
		t.Fatalf("taking the relay lock: %v", err)
	}
	return lease
}

func TestCommandsHandTheirPacketToTheSyncThatHasTheRelay(t *testing.T) {
	st := store.New(t.TempDir())
	type call struct {
		payload string
		qos     byte
		budget  time.Duration
	}
	calls := make(chan call, 1)
	startSync(t, context.Background(), st, controlHooks{publish: func(_ context.Context, payload []byte, qos byte, deadline time.Time) controlResponse {
		calls <- call{string(payload), qos, time.Until(deadline)}
		return controlResponse{OK: true, Sent: true, ServerID: "5:100"}
	}})
	serverID, via, err := PublishOnStore(context.Background(), st, noOwnConnection(t), []byte(`{"t":"m"}`))
	if err != nil || serverID != "5:100" || via != ViaSync {
		t.Fatalf("PublishOnStore = %q, %q, %v; want 5:100 through the sync", serverID, via, err)
	}
	got := <-calls
	if got.payload != `{"t":"m"}` || got.qos != 1 {
		t.Errorf("the sync published %q with QoS %d", got.payload, got.qos)
	}
	// The sync must be done before the command stops waiting for its answer.
	if got.budget <= 0 || got.budget > controlHandoffTimeout {
		t.Errorf("hand-off budget %v; want at most %v", got.budget, controlHandoffTimeout)
	}
	if connected, ok := SyncRelayStatus(context.Background(), st.ControlPath(), RelayOwnerPID(st)); !connected || !ok {
		t.Errorf("SyncRelayStatus = %v, %v; want connected", connected, ok)
	}
}

func TestCommandsConnectOnTheirOwnWhileNobodyHasTheRelay(t *testing.T) {
	st := store.New(t.TempDir())
	// A sync that died left its endpoint behind; the free relay lock shows that nobody has the relay.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	stale, _ := json.Marshal(controlEndpoint{V: controlProtocol, PID: os.Getpid(), Port: ln.Addr().(*net.TCPAddr).Port, Token: "t"})
	ln.Close()
	if err := store.WriteFileAtomic(st.ControlPath(), string(stale)); err != nil {
		t.Fatal(err)
	}
	var connected atomic.Bool
	_, via, err := PublishOnStore(context.Background(), st, ownConnection(t, st, &connected), []byte("{}"))
	if !errors.Is(err, errOwnConnection) || via != ViaOwn || !connected.Load() {
		t.Fatalf("via=%q err=%v; want a connection of its own", via, err)
	}
	takeRelay(t, st).Release() // the command let go of the relay
}

func TestCommandsNeverConnectWhileAnotherCommandHasTheRelay(t *testing.T) {
	shortBudget(t, 500*time.Millisecond)
	st := store.New(t.TempDir())
	defer takeRelay(t, st).Release()
	_, _, err := PublishOnStore(context.Background(), st, noOwnConnection(t), []byte("{}"))
	var unavailable *RelayUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("err=%v; want RelayUnavailableError (nothing sent)", err)
	}
}

func TestCommandsTakeTheRelayOnceItIsFree(t *testing.T) {
	shortBudget(t, 5*time.Second)
	st := store.New(t.TempDir())
	lease := takeRelay(t, st)
	var released atomic.Bool
	time.AfterFunc(300*time.Millisecond, func() { released.Store(true); lease.Release() })
	connect := func() (*api.Client, error) {
		if !released.Load() {
			t.Error("connected while another process had the relay")
		}
		return nil, errOwnConnection
	}
	if _, via, err := PublishOnStore(context.Background(), st, connect, []byte("{}")); !errors.Is(err, errOwnConnection) || via != ViaOwn {
		t.Fatalf("via=%q err=%v; want a connection of its own once the relay was free", via, err)
	}
}

func TestCommandsWaitForAReconnectingSync(t *testing.T) {
	shortBudget(t, 5*time.Second)
	st := store.New(t.TempDir())
	var calls atomic.Int32
	startSync(t, context.Background(), st, controlHooks{publish: func(context.Context, []byte, byte, time.Time) controlResponse {
		if calls.Add(1) < 3 {
			return controlResponse{Error: "sync is reconnecting to Hike", Stopping: true}
		}
		return controlResponse{OK: true, Sent: true, ServerID: "7:1"}
	}})
	serverID, via, err := PublishOnStore(context.Background(), st, noOwnConnection(t), []byte("{}"))
	if err != nil || serverID != "7:1" || via != ViaSync {
		t.Fatalf("PublishOnStore = %q, %q, %v; want 7:1 through the sync once it reconnected", serverID, via, err)
	}
}

func TestAStoppingSyncKeepsCommandsOffTheRelayUntilItLetsGo(t *testing.T) {
	// Its relay connection may still be finishing a publish while it stops.
	for _, how := range []string{"interrupted", "idle exit"} {
		t.Run(how, func(t *testing.T) {
			shortBudget(t, 5*time.Second)
			st := store.New(t.TempDir())
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var closing atomic.Bool
			stop := startSync(t, ctx, st, controlHooks{
				publish: func(context.Context, []byte, byte, time.Time) controlResponse {
					t.Error("a stopping sync took a new publish")
					return controlResponse{}
				},
				stopping: closing.Load,
			})
			if how == "interrupted" {
				cancel()
			} else {
				closing.Store(true)
			}
			var stopped atomic.Bool
			time.AfterFunc(300*time.Millisecond, func() { stopped.Store(true); stop() })
			connect := func() (*api.Client, error) {
				if !stopped.Load() {
					t.Error("connected before the stopping sync let go of the relay")
				}
				return nil, errOwnConnection
			}
			if _, via, err := PublishOnStore(context.Background(), st, connect, []byte("{}")); !errors.Is(err, errOwnConnection) || via != ViaOwn {
				t.Fatalf("via=%q err=%v; want a connection of its own once the sync had gone", via, err)
			}
		})
	}
}

func TestCommandsTellNothingToAnEndpointThatCannotProveItIsTheSync(t *testing.T) {
	// The sync died, its endpoint record stayed, and another program now listens on its port.
	shortBudget(t, 700*time.Millisecond)
	st := store.New(t.TempDir())
	defer takeRelay(t, st).Release()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	lines := make(chan string, 64)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				reader := bufio.NewReader(conn)
				for {
					line, err := reader.ReadString('\n')
					if err != nil {
						return
					}
					lines <- line
					_, _ = conn.Write([]byte(`{"v":2,"proof":"00","nonce":"abcd","ok":true,"sent":true,"server_id":"1:1"}` + "\n"))
				}
			}()
		}
	}()
	endpoint, _ := json.Marshal(controlEndpoint{V: controlProtocol, PID: os.Getpid(), Port: ln.Addr().(*net.TCPAddr).Port, Token: "the-token"})
	if err := store.WriteFileAtomic(st.ControlPath(), string(endpoint)); err != nil {
		t.Fatal(err)
	}
	_, _, err = PublishOnStore(context.Background(), st, noOwnConnection(t), []byte(`{"m":"the-payload"}`))
	var unavailable *RelayUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("err=%v; want RelayUnavailableError (nothing sent)", err)
	}
	hellos := 0
	for len(lines) > 0 {
		line := <-lines
		if strings.Contains(line, "the-token") || strings.Contains(line, "the-payload") || !strings.Contains(line, `"op":"hello"`) {
			t.Errorf("the command told an endpoint that did not prove itself %q", line)
		}
		hellos++
	}
	if hellos == 0 {
		t.Error("the command never tried the recorded endpoint")
	}
}

func TestCommandsReportAHandOffThatMayHaveGoneOut(t *testing.T) {
	st := store.New(t.TempDir())
	startSync(t, context.Background(), st, controlHooks{publish: func(context.Context, []byte, byte, time.Time) controlResponse {
		return controlResponse{Sent: true, Error: "Hike did not confirm delivery after 3 attempts"}
	}})
	_, via, err := PublishOnStore(context.Background(), st, noOwnConnection(t), []byte("{}"))
	var relayErr *mqtt.Error
	if via != ViaSync || !errors.As(err, &relayErr) || relayErr.Msg != "Hike did not confirm delivery after 3 attempts" {
		t.Fatalf("via=%q err=%v; want the sync's relay error, without a second attempt", via, err)
	}
}

func TestATooLargePacketIsRefusedBeforeAnythingIsSent(t *testing.T) {
	st := store.New(t.TempDir())
	startSync(t, context.Background(), st, controlHooks{publish: func(context.Context, []byte, byte, time.Time) controlResponse {
		t.Error("the sync was handed a packet that is too large")
		return controlResponse{}
	}})
	_, _, err := PublishOnStore(context.Background(), st, noOwnConnection(t), []byte(strings.Repeat("a", maxControlRequest)))
	var tooLarge *TooLargeError
	if !errors.As(err, &tooLarge) {
		t.Fatalf("err=%v; want TooLargeError", err)
	}
}

func TestTheEndpointActsOnlyOnAuthenticatedRequests(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sync-control.json")
	payloads := make(chan string, 4)
	stop, err := serveControl(context.Background(), path, controlHooks{
		publish: func(_ context.Context, payload []byte, _ byte, _ time.Time) controlResponse {
			payloads <- string(payload)
			return controlResponse{OK: true, Sent: true, ServerID: "1:1"}
		},
		connected: func() bool { return true },
		stopping:  func() bool { return false },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	endpoint, _ := readControlEndpoint(path)
	dial := func() (send func(any), receive func(any) error) {
		conn, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(endpoint.Port)))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.Close() })
		reader := bufio.NewReader(conn)
		send = func(v any) {
			if err := writeControlLine(conn, v); err != nil {
				t.Fatal(err)
			}
		}
		return send, func(v any) error { return readControlLine(reader, v) }
	}

	send, receive := dial()
	send(controlRequest{Op: "publish", Payload: "anonymous", QoS: 1})
	var resp controlResponse
	if err := receive(&resp); err != nil || resp.OK || resp.Sent || resp.Error != "not authorized" {
		t.Errorf("publish without a proof: %+v, %v; want not authorized", resp, err)
	}

	send, receive = dial()
	send(controlRequest{Op: "hello", Nonce: "n1"})
	var hello controlHello
	if err := receive(&hello); err != nil || hello.Proof != controlProof(endpoint.Token, "sync", "n1") || hello.Nonce == "" {
		t.Fatalf("hello: %+v, %v; want the sync's proof and a nonce", hello, err)
	}
	// The sync's own proof does not pass for a command's.
	send(controlRequest{Op: "publish", Auth: controlProof(endpoint.Token, "sync", hello.Nonce), Payload: "reflected", QoS: 1})
	resp = controlResponse{}
	if err := receive(&resp); err != nil || resp.OK || resp.Error != "not authorized" {
		t.Errorf("publish with a wrong proof: %+v, %v; want not authorized", resp, err)
	}

	// hkcli 0.4.0 commands send the secret itself.
	send, receive = dial()
	send(controlRequest{Op: "publish", Token: endpoint.Token, Payload: "from 0.4.0", QoS: 1})
	resp = controlResponse{}
	if err := receive(&resp); err != nil || !resp.OK || resp.ServerID != "1:1" {
		t.Errorf("publish from hkcli 0.4.0: %+v, %v; want it published", resp, err)
	}
	if got := <-payloads; got != "from 0.4.0" {
		t.Errorf("published %q; want only the authenticated payload", got)
	}
	if len(payloads) > 0 {
		t.Errorf("published %q without authentication", <-payloads)
	}
}

func TestControlPublishAnswersAtOnceWhenNoConnectionIsComing(t *testing.T) {
	deadline := time.Now().Add(time.Minute)
	interrupted, cancel := context.WithCancel(context.Background())
	cancel()
	for name, tc := range map[string]struct {
		s   *Syncer
		ctx context.Context
	}{
		"stopping for good": {&Syncer{stopping: true, ownsRelay: true}, context.Background()},
		"interrupted":       {&Syncer{ownsRelay: true}, interrupted},
		// Waiting to reconnect without the relay lock: the command may connect on its own.
		"between connections": {&Syncer{}, context.Background()},
	} {
		start := time.Now()
		resp := tc.s.controlPublish(tc.ctx, []byte("{}"), 1, deadline)
		if !resp.Stopping || resp.Sent || resp.OK {
			t.Errorf("%s: %+v; want try again, nothing sent", name, resp)
		}
		if waited := time.Since(start); waited > time.Second {
			t.Errorf("%s: answered after %v; want at once", name, waited)
		}
	}
}

func TestControlPublishWaitsForTheConnectionOfASyncThatHasTheRelay(t *testing.T) {
	start := time.Now()
	resp := (&Syncer{ownsRelay: true}).controlPublish(context.Background(), []byte("{}"), 1, start.Add(300*time.Millisecond))
	waited := time.Since(start)
	if !resp.Stopping || resp.Sent || resp.OK {
		t.Errorf("%+v; want try again, nothing sent", resp)
	}
	if waited < 250*time.Millisecond || waited > 2*time.Second {
		t.Errorf("waited %v; want until the command's deadline (300ms)", waited)
	}
}

func TestStopRemovesOnlyItsOwnEndpoint(t *testing.T) {
	hooks := controlHooks{
		publish:   func(context.Context, []byte, byte, time.Time) controlResponse { return controlResponse{} },
		connected: func() bool { return true },
		stopping:  func() bool { return false },
	}
	path := filepath.Join(t.TempDir(), "sync-control.json")
	stop, err := serveControl(context.Background(), path, hooks)
	if err != nil {
		t.Fatal(err)
	}
	stop()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stop left its endpoint behind: %v", err)
	}

	stop, err = serveControl(context.Background(), path, hooks)
	if err != nil {
		t.Fatal(err)
	}
	newer := `{"v":2,"pid":2,"port":1,"token":"newer"}`
	if err := os.WriteFile(path, []byte(newer), 0o600); err != nil {
		t.Fatal(err)
	}
	stop()
	if data, err := os.ReadFile(path); err != nil || string(data) != newer {
		t.Errorf("endpoint file after stop = %q, %v; want the newer sync's", data, err)
	}
}

func TestASyncWaitingForTheRelayGivesUpWhenItsLimitPasses(t *testing.T) {
	st := store.New(t.TempDir())
	defer takeRelay(t, st).Release() // a command keeps the relay
	s := &Syncer{out: NewEmitter(true, true)}
	checks := 0
	_, err := s.takeRelay(context.Background(), st, func() bool { checks++; return checks == 2 })
	var unavailable *RelayUnavailableError
	if !errors.As(err, &unavailable) || checks != 2 || s.ownsRelay {
		t.Fatalf("takeRelay = %v after %d checks (owns relay: %v); want RelayUnavailableError after 2", err, checks, s.ownsRelay)
	}
}

func TestOpeningARelayConnectionNeedsTheRelayLock(t *testing.T) {
	released := takeRelay(t, store.New(t.TempDir()))
	released.Release()
	for name, lease := range map[string]*RelayLease{"no lease": nil, "released lease": released} {
		if _, err := OpenMQTT(context.Background(), nil, lease); err == nil {
			t.Errorf("%s: OpenMQTT did not refuse", name)
		}
	}
}

func TestACommandOnItsOwnConnectionKeepsToItsBudget(t *testing.T) {
	// Hike's relay takes the connection but never answers.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			if _, err := ln.Accept(); err != nil {
				return
			}
		}
	}()
	t.Setenv("HKCLI_MQTT_WS", "ws://"+ln.Addr().String()+"/mqtt")
	saved := publishBudget
	publishBudget = controlTeardownTimeout + 300*time.Millisecond
	t.Cleanup(func() { publishBudget = saved })
	st := store.New(t.TempDir())
	connect := func() (*api.Client, error) {
		return api.New(st, &store.Session{UserID: "1001", AccessToken: "t"}), nil
	}
	start := time.Now()
	_, via, err := PublishOnStore(context.Background(), st, connect, []byte("{}"))
	var unavailable *RelayUnavailableError
	if via != ViaOwn || !errors.As(err, &unavailable) {
		t.Fatalf("via=%q err=%v; want RelayUnavailableError (nothing sent) on its own connection", via, err)
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Errorf("took %v; want it to give up within its budget", took)
	}
}
