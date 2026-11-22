package mqtt

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/vinaycodeaxe/hkcli/internal/config"
)

// TestLiveRelayRefusesBadToken talks to Hike's real relay; run it with HKCLI_LIVE=1.
func TestLiveRelayRefusesBadToken(t *testing.T) {
	if os.Getenv("HKCLI_LIVE") != "1" {
		t.Skip("set HKCLI_LIVE=1 to test against Hike's relay")
	}
	c := &Client{
		URL:       config.MQTTWSURL(),
		ClientID:  fmt.Sprintf("web_0_%d", time.Now().UnixMilli()),
		Username:  "0",
		Password:  "invalid-token",
		Keepalive: 25 * time.Second,
		Origin:    config.WebOrigin,
		UserAgent: config.UserAgent(),
	}
	err := c.Connect(context.Background())
	if err == nil {
		c.Disconnect()
		t.Fatal("the relay accepted an invalid token")
	}
	if !IsKind(err, KindAuth) {
		t.Fatalf("want a refused login (CONNACK 4/5), got: %v", err)
	}
	t.Logf("relay answered: %v", err)
}
