package protocol

import (
	"reflect"
	"testing"

	"github.com/vinaycodeaxe/hkcli/internal/pyjson"
)

func TestConversationIDFollowsHikeWebRules(t *testing.T) {
	cases := []struct {
		from, to, want string
		ok             bool
	}{
		{"2", "1", "2", true},
		{"1", "2", "2", true},
		{"3", "g9", "g9", true},
		{"2", "u:1", "2", true},
		{"1", "1", "", false},
	}
	for _, c := range cases {
		got, ok := ConversationID(pyjson.O("f", c.from, "to", c.to), "1")
		if got != c.want || ok != c.ok {
			t.Errorf("ConversationID(f=%s, to=%s) = %q, %v; want %q, %v", c.from, c.to, got, ok, c.want, c.ok)
		}
	}
}

func TestHistoryItemMetadataAndMedia(t *testing.T) {
	packet := pyjson.O("t", "m", "st", "img", "f", "2", "to", "1", "i", "c1", "s", "7:99", "ts", "99", "d", pyjson.O("p", "file-9", "m", "cap"))
	item := pyjson.O("messageJson", pyjson.DumpsCompact(packet), "state", "READ", "starred", true, "reactionMap", pyjson.O("1", "😂"))
	msg := ParseMessage(PacketFromHistoryItem(item), "1", item, "")
	got := []any{msg.ChatID, msg.Kind, msg.FileID, msg.Text, msg.TS, msg.State}
	if want := []any{"2", "image", "file-9", "cap", int64(99), "read"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("parsed %v, want %v", got, want)
	}
	if msg.Starred == nil || !*msg.Starred || !reflect.DeepEqual(msg.Reactions, []Reaction{{"1", "😂"}}) {
		t.Fatalf("starred %v, reactions %v", msg.Starred, msg.Reactions)
	}
	if msg.Edited != nil {
		t.Fatal("edited should be unknown, so a live packet can never clear the flag")
	}
}

func TestIDsMayBeNumbers(t *testing.T) {
	if got := AsStr(1001.0); got != "1001" {
		t.Fatalf("AsStr(1001.0) = %q", got)
	}
	msg := ParseMessage(pyjson.O("t", "m", "f", int64(2), "to", int64(1), "ts", 5.0, "d", pyjson.O("m", "hi")), "1", nil, "")
	if msg.ChatID != "2" || msg.TS != 5 || msg.FromMe {
		t.Fatalf("parsed %+v", msg)
	}
}

func TestGroupMentions(t *testing.T) {
	md := []any{pyjson.O("uid", "1", "num", "+919800000001", "start", 0.0, "length", 6.0), pyjson.O("start", 7.0), "junk"}
	msg := ParseMessage(pyjson.O("t", "m", "f", "2", "to", "g9", "ts", 5.0, "d", pyjson.O("m", "@Vinay lunch?", "md", md)), "1", nil, "")
	want := []Mention{{UserID: "1", Phone: "+919800000001", Start: 0, Length: 6}}
	if msg.ChatID != "g9" || !reflect.DeepEqual(msg.Mentions, want) {
		t.Fatalf("chat %q, mentions %+v; want g9, %+v", msg.ChatID, msg.Mentions, want)
	}
}
