package mqtt

import (
	"bytes"
	"encoding/hex"
	"testing"
)

func unhex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRemainingLengthMatchesMQTTSpecTable(t *testing.T) {
	cases := map[int]string{0: "00", 127: "7f", 128: "8001", 16383: "ff7f", 16384: "808001", 2097151: "ffff7f", 268435455: "ffffff7f"}
	for value, want := range cases {
		got, err := EncodeRemainingLength(value)
		if err != nil || !bytes.Equal(got, unhex(t, want)) {
			t.Errorf("EncodeRemainingLength(%d) = %x, %v; want %s", value, got, err, want)
		}
	}
}

func TestConnectPacketBytes(t *testing.T) {
	want := unhex(t, "1013"+"00044d515454"+"04"+"c2"+"0019"+"000163"+"000175"+"000170")
	if got := EncodeConnect("c", "u", "p", 25); !bytes.Equal(got, want) {
		t.Fatalf("CONNECT = %x, want %x", got, want)
	}
}

func TestReaderReassemblesFramesAndReadsHikePubackPayload(t *testing.T) {
	puback := unhex(t, "400a0005000631323a333435") // PUBACK id 5 + Hike payload "12:345"
	var r PacketReader
	if packets, err := r.Feed(puback[:3]); err != nil || len(packets) != 0 {
		t.Fatalf("partial frame gave %v, %v", packets, err)
	}
	packets, err := r.Feed(append(puback[3:], unhex(t, "d000")...))
	if err != nil || len(packets) != 2 || packets[0].Type != PUBACK || packets[1].Type != PINGRESP {
		t.Fatalf("packets = %+v, %v", packets, err)
	}
	if id, payload, err := ParsePuback(packets[0].Body); err != nil || id != 5 || string(payload) != "12:345" {
		t.Fatalf("ParsePuback = %d, %q, %v", id, payload, err)
	}
	if id, payload, err := ParsePuback(unhex(t, "0005")); err != nil || id != 5 || payload != nil {
		t.Fatalf("ParsePuback without payload = %d, %q, %v", id, payload, err)
	}
}
