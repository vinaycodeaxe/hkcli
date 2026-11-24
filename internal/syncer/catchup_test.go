package syncer

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/vinaycodeaxe/hkcli/internal/db"
	"github.com/vinaycodeaxe/hkcli/internal/pyjson"
)

// historyItem is a msg-history item holding a text message.
func historyItem(from, to string, seq int, ts int64) any {
	packet := fmt.Sprintf(`{"t":"m","st":"text","f":%q,"to":%q,"i":"id-%d","s":"%d:%d","ts":%d,"d":{"st":"text","ts":%d,"m":"m%d"}}`,
		from, to, seq, seq, ts, ts, ts, seq)
	return pyjson.O("messageJson", packet, "state", "DELIVERED")
}

func TestCatchUpCountsMessagesTheListeningConnectionMissed(t *testing.T) {
	conn, err := db.Connect(filepath.Join(t.TempDir(), "hike.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	s := New(conn, nil, "1001", NewEmitter(true, true), DefaultOptions())
	items := []any{
		historyItem("2002", "1001", 1, 500),  // before the connection listened
		historyItem("2002", "1001", 2, 2000), // while it listened: it should have arrived live
		historyItem("1001", "2002", 3, 3000), // sent from this account
		historyItem("2002", "1001", 4, 6000), // after the connection ended
	}
	missed := span{from: 1000, to: 5000}
	added, known, late, err := s.storeHistory("2002", items, missed)
	if err != nil || added != 4 || known != 0 || late != 1 {
		t.Fatalf("storeHistory = %d added, %d known, %d late, %v; want 4, 0, 1", added, known, late, err)
	}
	if added, known, late, err := s.storeHistory("2002", items, missed); err != nil || added != 0 || known != 4 || late != 0 {
		t.Errorf("storing them again = %d added, %d known, %d late, %v; want 0, 4, 0", added, known, late, err)
	}
}
