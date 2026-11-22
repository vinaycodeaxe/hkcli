import json

import pytest

from hikecli import db
from hikecli import mqtt as m
from hikecli.cli import safe_filename
from hikecli.phone import split_phone
from hikecli.protocol import Message, conversation_id, packet_from_history_item, parse_message


def test_remaining_length_matches_mqtt_spec_table():
    cases = {0: "00", 127: "7f", 128: "8001", 16383: "ff7f", 16384: "808001", 2097151: "ffff7f", 268435455: "ffffff7f"}
    for value, encoded in cases.items():
        assert m.encode_remaining_length(value) == bytes.fromhex(encoded)


def test_connect_packet_bytes():
    expected = "1013" + "00044d515454" + "04" + "c2" + "0019" + "000163" + "000175" + "000170"
    assert m.encode_connect("c", "u", "p", keepalive=25) == bytes.fromhex(expected)


def test_reader_reassembles_frames_and_reads_hike_puback_payload():
    puback = bytes.fromhex("400a0005000631323a333435")  # PUBACK id 5 + Hike payload "12:345"
    reader = m.PacketReader()
    assert reader.feed(puback[:3]) == []
    packets = reader.feed(puback[3:] + bytes.fromhex("d000"))
    assert [p.type for p in packets] == [m.PUBACK, m.PINGRESP]
    assert m.parse_puback(packets[0].body) == (5, b"12:345")
    assert m.parse_puback(bytes.fromhex("0005")) == (5, None)


def test_conversation_id_follows_hike_web_rules():
    assert conversation_id({"f": "2", "to": "1"}, "1") == "2"
    assert conversation_id({"f": "1", "to": "2"}, "1") == "2"
    assert conversation_id({"f": "3", "to": "g9"}, "1") == "g9"
    assert conversation_id({"f": "2", "to": "u:1"}, "1") == "2"
    assert conversation_id({"f": "1", "to": "1"}, "1") is None


def test_history_item_metadata_and_media():
    packet = {"t": "m", "st": "img", "f": "2", "to": "1", "i": "c1", "s": "7:99", "ts": "99", "d": {"p": "file-9", "m": "cap"}}
    item = {"messageJson": json.dumps(packet), "state": "READ", "starred": True, "reactionMap": {"1": "😂"}}
    msg = parse_message(packet_from_history_item(item), "1", meta=item)
    assert (msg.chat_id, msg.kind, msg.file_id, msg.text, msg.ts, msg.state) == ("2", "image", "file-9", "cap", 99, "read")
    assert msg.starred is True and msg.reactions == {"1": "😂"}
    assert msg.edited is None  # unknown, so a live packet can never clear a flag


@pytest.mark.parametrize(
    "value, expected",
    [
        ("+919876543210", ("+91", "9876543210")),
        ("+91 98765 43210", ("+91", "9876543210")),
        ("+14155550100", ("+1", "4155550100")),
        ("+12685551234", ("+1268", "5551234")),
    ],
)
def test_split_phone(value, expected):
    assert split_phone(value) == expected


def test_split_phone_needs_country_code():
    with pytest.raises(ValueError):
        split_phone("9876543210")


def _msg(**fields):
    base = dict(chat_id="2", sender_id="1", from_me=True, ts=10, kind="text")
    base.update(fields)
    return Message(**base)


def test_pending_send_merges_with_server_echo_and_never_downgrades(tmp_path):
    conn = db.connect(tmp_path / "t.db")
    assert db.upsert_message(conn, _msg(client_id="c1", text="hi", state="pending"))[1] is True
    echo = _msg(client_id="c1", server_id="5:10", text="hi", state="sent")
    assert db.upsert_message(conn, echo)[1] is False
    db.mark_my_messages(conn, "2", 5, "read")
    db.upsert_message(conn, echo)
    rows = [tuple(r) for r in conn.execute("SELECT server_id, client_id, state FROM messages")]
    assert rows == [("5:10", "c1", "read")]


def test_call_updates_share_one_row(tmp_path):
    conn = db.connect(tmp_path / "t.db")
    start = parse_message({"t": "v", "f": "2", "to": "1", "ts": 1, "d": {"st": "cinit", "rid": "r1", "mt": "video"}}, "1")
    end = parse_message({"t": "v", "f": "2", "to": "1", "ts": 2, "d": {"st": "ce", "rid": "r1", "mt": "video", "dur": 75}}, "1")
    db.upsert_message(conn, start)
    db.upsert_message(conn, end)
    assert [r["text"] for r in conn.execute("SELECT text FROM messages")] == ["video call ended 1m 15s"]


def test_receipts_are_cumulative_by_sequence(tmp_path):
    conn = db.connect(tmp_path / "t.db")
    for seq in (3, 4, 9):
        db.upsert_message(conn, _msg(ts=seq, server_id=f"{seq}:{seq}", state="sent"))
    assert db.mark_my_messages(conn, "2", 4, "delivered") == 2
    assert [r[0] for r in conn.execute("SELECT state FROM messages ORDER BY ts")] == ["delivered", "delivered", "sent"]


def test_search_index_follows_edits_and_deletes(tmp_path):
    conn = db.connect(tmp_path / "t.db")
    db.upsert_message(conn, _msg(from_me=False, sender_id="2", server_id="1:1", text="quarterly report draft"))
    assert [r["text"] for r in db.search_messages(conn, "report")] == ["quarterly report draft"]
    db.apply_message_action(conn, "2", "em", "1:1", text="final numbers")
    assert db.search_messages(conn, "report") == [] and len(db.search_messages(conn, "numb*")) == 1
    db.apply_message_action(conn, "2", "dma", "1:1")
    assert db.search_messages(conn, "final") == []
    assert db.search_messages(conn, 'say "hi"') == []  # quotes are escaped, not an FTS syntax error


@pytest.mark.parametrize(
    "remote, safe",
    [("../../evil.exe", "evil.exe"), ("a\\b\\..\\c.txt", "c.txt"), ("CON.txt", "_CON.txt"), ("nul", "_nul"), ("..", "file"), ('re:port?.pdf', "re_port_.pdf")],
)
def test_remote_file_names_are_made_safe(remote, safe):
    assert safe_filename(remote) == safe
