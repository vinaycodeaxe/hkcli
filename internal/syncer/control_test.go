package syncer

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vinaycodeaxe/hkcli/internal/mqtt"
)

type controlPublisher = func(ctx context.Context, payload []byte, qos byte) controlResponse

func startControl(t *testing.T, publish controlPublisher) (path string, stop func()) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "sync-control.json")
	hooks := controlHooks{publish: publish, connected: func() bool { return true }, stopping: func() bool { return false }}
	stop, err := serveControl(context.Background(), path, hooks)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	return path, stop
}

func TestPublishViaSyncUsesTheRunningSync(t *testing.T) {
	var gotPayload string
	var gotQoS byte
	path, stop := startControl(t, func(_ context.Context, payload []byte, qos byte) controlResponse {
		gotPayload, gotQoS = string(payload), qos
		return controlResponse{OK: true, Sent: true, ServerID: "5:100"}
	})
	serverID, handled, err := PublishViaSync(context.Background(), path, []byte(`{"t":"m"}`), 1)
	if err != nil || !handled || serverID != "5:100" {
		t.Fatalf("PublishViaSync = %q, %v, %v; want 5:100, true, nil", serverID, handled, err)
	}
	if gotPayload != `{"t":"m"}` || gotQoS != 1 {
		t.Errorf("sync published %q with QoS %d", gotPayload, gotQoS)
	}
	if connected, ok, pid := SyncRelayStatus(context.Background(), path); !connected || !ok || pid != os.Getpid() {
		t.Errorf("SyncRelayStatus = %v, %v, %d", connected, ok, pid)
	}
	stop()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stop left the endpoint file behind: %v", err)
	}
	if _, handled, _ := PublishViaSync(context.Background(), path, []byte("{}"), 1); handled {
		t.Error("a stopped sync still took the publish")
	}
}

func TestPublishViaSyncFallsBackWhenNothingWasSent(t *testing.T) {
	notConnected, _ := startControl(t, func(context.Context, []byte, byte) controlResponse {
		return controlResponse{Error: "sync is not connected to Hike right now"}
	})

	wrongToken, _ := startControl(t, func(context.Context, []byte, byte) controlResponse {
		t.Error("published despite a wrong token")
		return controlResponse{OK: true, Sent: true}
	})
	endpoint, _ := readControlEndpoint(wrongToken)
	endpoint.Token = "not-the-token"
	data, _ := json.Marshal(endpoint)
	if err := os.WriteFile(wrongToken, data, 0o600); err != nil {
		t.Fatal(err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedPort := filepath.Join(t.TempDir(), "sync-control.json")
	data, _ = json.Marshal(controlEndpoint{PID: 1, Port: ln.Addr().(*net.TCPAddr).Port, Token: "t"})
	ln.Close()
	if err := os.WriteFile(closedPort, data, 0o600); err != nil {
		t.Fatal(err)
	}

	for name, path := range map[string]string{
		"no sync":       filepath.Join(t.TempDir(), "sync-control.json"),
		"sync exited":   closedPort,
		"wrong token":   wrongToken,
		"not connected": notConnected,
	} {
		if _, handled, err := PublishViaSync(context.Background(), path, []byte("{}"), 1); handled || err != nil {
			t.Errorf("%s: handled=%v err=%v; want a fallback", name, handled, err)
		}
	}
}

func TestPublishViaSyncReportsAPublishThatMayHaveGoneOut(t *testing.T) {
	path, _ := startControl(t, func(context.Context, []byte, byte) controlResponse {
		return controlResponse{Sent: true, Error: "Hike did not confirm delivery after 3 attempts"}
	})
	_, handled, err := PublishViaSync(context.Background(), path, []byte("{}"), 1)
	var relayErr *mqtt.Error
	if !handled || !errors.As(err, &relayErr) || relayErr.Msg != "Hike did not confirm delivery after 3 attempts" {
		t.Fatalf("handled=%v err=%v; want the sync's relay error, without a second attempt", handled, err)
	}
}

func TestPublishWaitsUntilAStoppingSyncHasGone(t *testing.T) {
	// The sync's relay connection may still be finishing a publish while it stops.
	for _, how := range []string{"interrupted", "idle exit"} {
		t.Run(how, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var closing atomic.Bool
			path := filepath.Join(t.TempDir(), "sync-control.json")
			stop, err := serveControl(ctx, path, controlHooks{
				publish: func(context.Context, []byte, byte) controlResponse {
					t.Error("a stopping sync took a new publish")
					return controlResponse{}
				},
				connected: func() bool { return true },
				stopping:  closing.Load,
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(stop)
			if how == "interrupted" {
				cancel()
			} else {
				closing.Store(true)
			}
			var stopped atomic.Bool
			time.AfterFunc(300*time.Millisecond, func() { stopped.Store(true); stop() })
			_, handled, err := PublishViaSync(context.Background(), path, []byte("{}"), 1)
			if handled || err != nil || !stopped.Load() {
				t.Fatalf("handled=%v err=%v stopped=%v; want a fallback only once the sync has gone", handled, err, stopped.Load())
			}
		})
	}
}

func TestControlPublishSaysStoppingOnceTheSyncCloses(t *testing.T) {
	// A request admitted just before the sync began to close its relay connection.
	closing := &Syncer{stopping: true}
	if resp := closing.controlPublish(context.Background(), []byte("{}"), 1); !resp.Stopping || resp.Sent {
		t.Errorf("closing for good: %+v; want stopping, nothing sent", resp)
	}
	interrupted, cancel := context.WithCancel(context.Background())
	cancel()
	if resp := (&Syncer{}).controlPublish(interrupted, []byte("{}"), 1); !resp.Stopping || resp.Sent {
		t.Errorf("interrupted: %+v; want stopping, nothing sent", resp)
	}
}

func TestStopKeepsANewerSyncsEndpoint(t *testing.T) {
	path, stop := startControl(t, func(context.Context, []byte, byte) controlResponse { return controlResponse{} })
	newer := `{"pid":2,"port":1,"token":"newer"}`
	if err := os.WriteFile(path, []byte(newer), 0o600); err != nil {
		t.Fatal(err)
	}
	stop()
	if data, err := os.ReadFile(path); err != nil || string(data) != newer {
		t.Errorf("endpoint file after stop = %q, %v; want the newer sync's", data, err)
	}
}
