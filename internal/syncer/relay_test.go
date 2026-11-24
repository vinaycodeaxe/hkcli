package syncer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/vinaycodeaxe/hkcli/internal/api"
	"github.com/vinaycodeaxe/hkcli/internal/mqtt"
	"github.com/vinaycodeaxe/hkcli/internal/store"
)

// fakeRelay is a minimal Hike relay for one connection: it accepts the login and takes
// publishes without ever acknowledging them. With stall, it stops reading after the login, so
// the client's writes back up.
type fakeRelay struct {
	published chan struct{} // a publish arrived
	closed    chan struct{} // the connection ended
}

func startFakeRelay(t *testing.T, stall bool) *fakeRelay {
	t.Helper()
	relay := &fakeRelay{published: make(chan struct{}, 16), closed: make(chan struct{})}
	release := make(chan struct{})
	var taken atomic.Bool
	upgrader := websocket.Upgrader{Subprotocols: []string{"mqtt"}, CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		if !taken.CompareAndSwap(false, true) {
			return
		}
		defer close(relay.closed)
		for {
			_, frame, err := ws.ReadMessage()
			if err != nil || len(frame) == 0 {
				return
			}
			switch frame[0] >> 4 {
			case 1: // CONNECT
				_ = ws.WriteMessage(websocket.BinaryMessage, []byte{0x20, 2, 0, 0})
				if stall {
					<-release
					return
				}
			case 3: // PUBLISH
				relay.published <- struct{}{}
			case 14: // DISCONNECT
				return
			}
		}
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(release) }) // runs first
	t.Setenv("HKCLI_MQTT_WS", "ws"+strings.TrimPrefix(server.URL, "http")+"/mqtt")
	return relay
}

func shortTeardown(t *testing.T, d time.Duration) {
	drain, teardown := controlDrainTimeout, controlTeardownTimeout
	controlDrainTimeout, controlTeardownTimeout = d, d
	t.Cleanup(func() { controlDrainTimeout, controlTeardownTimeout = drain, teardown })
}

func connectFakeRelay(t *testing.T, st *store.Store, lease *RelayLease) *mqtt.Client {
	t.Helper()
	client, err := OpenMQTT(context.Background(), api.New(st, &store.Session{UserID: "1001", AccessToken: "t"}), lease)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestClosingDropsAConnectionWhoseHandOffOutlastsTheDrain(t *testing.T) {
	shortTeardown(t, 300*time.Millisecond)
	relay := startFakeRelay(t, false)
	st := store.New(t.TempDir())
	lease := takeRelay(t, st)
	defer lease.Release()
	client := connectFakeRelay(t, st, lease)
	s := &Syncer{me: "1001", out: NewEmitter(true, true)}
	s.setClient(client)
	s.setOwnsRelay(true)
	answer := make(chan controlResponse, 1)
	go func() {
		answer <- s.controlPublish(context.Background(), []byte(`{"t":"m"}`), 1, time.Now().Add(time.Minute))
	}()
	select {
	case <-relay.published:
	case <-time.After(5 * time.Second):
		t.Fatal("the hand-off never reached the relay")
	}
	s.detachClient(true)
	start := time.Now()
	s.closeRelay(client) // Hike never confirms the hand-off
	if took := time.Since(start); took > 3*time.Second {
		t.Errorf("closing took %v; want about the drain time", took)
	}
	if client.Connected() {
		t.Fatal("the connection was still open after closing, so letting go of the relay lock now would be unsafe")
	}
	select {
	case <-relay.closed:
	case <-time.After(2 * time.Second):
		t.Error("the relay never saw the connection close")
	}
	if resp := <-answer; resp.OK || !resp.Sent {
		t.Errorf("hand-off answer %+v; want a failure that may have gone out", resp)
	}
}

func TestTheGoodbyeCannotKeepTheConnectionOpen(t *testing.T) {
	shortTeardown(t, 300*time.Millisecond)
	startFakeRelay(t, true)
	st := store.New(t.TempDir())
	lease := takeRelay(t, st)
	defer lease.Release()
	client := connectFakeRelay(t, st, lease)
	stuck := make(chan error, 1)
	go func() {
		_, err := client.Publish(context.Background(), "1001", make([]byte, 32<<20), 0, 0)
		stuck <- err
	}()
	select {
	case <-stuck:
		t.Skip("the socket buffers took the whole write, so nothing holds the connection")
	case <-time.After(500 * time.Millisecond):
	}
	s := &Syncer{me: "1001", out: NewEmitter(true, true)}
	s.setClient(client)
	s.detachClient(true)
	start := time.Now()
	s.closeRelay(client) // no hand-offs, but the goodbye waits behind the stuck write
	if took := time.Since(start); took > 3*time.Second {
		t.Errorf("closing took %v; want about the teardown time", took)
	}
	if client.Connected() {
		t.Error("the connection was still open after closing")
	}
	if err := <-stuck; err == nil {
		t.Error("the stuck write succeeded although the connection was dropped")
	}
}
