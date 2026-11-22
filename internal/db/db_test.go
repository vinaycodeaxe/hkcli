package db

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/vinaycodeaxe/hkcli/internal/protocol"
	"github.com/vinaycodeaxe/hkcli/internal/pyjson"
)

func open(t *testing.T) *Conn {
	t.Helper()
	c, err := Connect(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// msg mirrors the Python tests' _msg defaults: chat "2", sender "1", ts 10, text.
func msg(m protocol.Message) *protocol.Message {
	m.ChatID, m.Kind = "2", "text"
	if m.SenderID == "" {
		m.SenderID = "1"
	}
	if m.TS == 0 {
		m.TS = 10
	}
	return &m
}

func upsert(t *testing.T, c *Conn, m *protocol.Message) bool {
	t.Helper()
	_, inserted, err := c.UpsertMessage(m)
	if err != nil {
		t.Fatal(err)
	}
	return inserted
}

func texts(t *testing.T, rows []Row, err error) []any {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	out := []any{}
	for _, r := range rows {
		out = append(out, r["text"])
	}
	return out
}

func TestPendingSendMergesWithServerEchoAndNeverDowngrades(t *testing.T) {
	c := open(t)
	if !upsert(t, c, msg(protocol.Message{FromMe: true, ClientID: "c1", Text: "hi", State: "pending"})) {
		t.Fatal("first upsert should insert")
	}
	echo := msg(protocol.Message{FromMe: true, ClientID: "c1", ServerID: "5:10", Text: "hi", State: "sent"})
	if upsert(t, c, echo) {
		t.Fatal("the echo should merge into the pending row")
	}
	if _, err := c.MarkMyMessages("2", 5, "read"); err != nil {
		t.Fatal(err)
	}
	upsert(t, c, echo)
	rows, err := c.query("SELECT server_id, client_id, state FROM messages")
	if err != nil {
		t.Fatal(err)
	}
	want := []Row{{"server_id": "5:10", "client_id": "c1", "state": "read"}}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("rows = %v, want %v", rows, want)
	}
}

func TestCallUpdatesShareOneRow(t *testing.T) {
	c := open(t)
	start := protocol.ParseMessage(pyjson.O("t", "v", "f", "2", "to", "1", "ts", int64(1), "d", pyjson.O("st", "cinit", "rid", "r1", "mt", "video")), "1", nil, "")
	end := protocol.ParseMessage(pyjson.O("t", "v", "f", "2", "to", "1", "ts", int64(2), "d", pyjson.O("st", "ce", "rid", "r1", "mt", "video", "dur", int64(75))), "1", nil, "")
	upsert(t, c, start)
	upsert(t, c, end)
	rows, err := c.query("SELECT text FROM messages")
	if got := texts(t, rows, err); !reflect.DeepEqual(got, []any{"video call ended 1m 15s"}) {
		t.Fatalf("texts = %v", got)
	}
}

func TestReceiptsAreCumulativeBySequence(t *testing.T) {
	c := open(t)
	for _, seq := range []int64{3, 4, 9} {
		id := pyjson.Str(seq) + ":" + pyjson.Str(seq)
		upsert(t, c, msg(protocol.Message{FromMe: true, TS: seq, ServerID: id, State: "sent"}))
	}
	n, err := c.MarkMyMessages("2", 4, "delivered")
	if err != nil || n != 2 {
		t.Fatalf("updated %d (%v), want 2", n, err)
	}
	rows, err := c.query("SELECT state FROM messages ORDER BY ts")
	if err != nil {
		t.Fatal(err)
	}
	var states []any
	for _, r := range rows {
		states = append(states, r["state"])
	}
	if !reflect.DeepEqual(states, []any{"delivered", "delivered", "sent"}) {
		t.Fatalf("states = %v", states)
	}
}

func TestSearchIndexFollowsEditsAndDeletes(t *testing.T) {
	c := open(t)
	upsert(t, c, msg(protocol.Message{SenderID: "2", ServerID: "1:1", Text: "quarterly report draft"}))
	search := func(q string) []any {
		rows, err := c.SearchMessages(q, "", 50)
		return texts(t, rows, err)
	}
	if got := search("report"); !reflect.DeepEqual(got, []any{"quarterly report draft"}) {
		t.Fatalf("search report = %v", got)
	}
	if _, err := c.ApplyMessageAction("2", "em", "1:1", "final numbers", nil, false); err != nil {
		t.Fatal(err)
	}
	if got := search("report"); len(got) != 0 {
		t.Fatalf("edited text still found: %v", got)
	}
	if got := search("numb*"); len(got) != 1 {
		t.Fatalf("prefix search = %v", got)
	}
	if _, err := c.ApplyMessageAction("2", "dma", "1:1", nil, nil, false); err != nil {
		t.Fatal(err)
	}
	if got := search("final"); len(got) != 0 {
		t.Fatalf("deleted text still found: %v", got)
	}
	if got := search(`say "hi"`); len(got) != 0 { // quotes are escaped, not an FTS syntax error
		t.Fatalf("quoted search = %v", got)
	}
}
