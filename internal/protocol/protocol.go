// Package protocol builds and parses Hike Web message packets.
//
// The same JSON packet format is used live over MQTT and inside msg-history REST
// items (wrapped as {"messageJson": "<packet>", "state": ..., ...}). Field names
// are Hike's short keys: t=type, st=subtype, f=from, to=to, c=conversation,
// i=client id, s=server id ("seq:timestamp"), ts=epoch ms, d=data.
package protocol

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/vinaycodeaxe/hkcli/internal/pyjson"
	"github.com/vinaycodeaxe/hkcli/internal/uuid"
)

// Packet types ("t").
const (
	TMessage            = "m"
	TDelivery           = "dr"
	TFirstDelivery      = "fdr"
	TMarkRead           = "mr"
	TFirstMarkRead      = "fmr"
	TGroupAction        = "gca"
	TCall               = "v"
	TConversationAction = "ca"
	TMessageAction      = "ma"
	TReaction           = "r"
	TInvite             = "inv"
	TStartTyping        = "st"
	TEndTyping          = "et"
	TForeground         = "fg"
	TBackground         = "bg"
	TPresence           = "pr"
	TDataSync           = "ds"
)

// Subtypes ("st").
const (
	STText           = "text"
	STFile           = "file"
	STAddReaction    = "ar"
	STRemoveReaction = "rr"
	STGroupInvite    = "gi"
)

// Message actions ("ma" d.st).
const (
	MAEdit         = "em"
	MADeleteForMe  = "dm"
	MADeleteForAll = "dma"
	MAStar         = "sm"
	MAPin          = "pm"
)

// Conversation actions ("ca" d.st).
const (
	CAClear   = "cc"
	CAArchive = "ac"
	CADelete  = "dc"
	CAMute    = "mc"
	CABlock   = "bc"
	CAUnread  = "uc"
	CAPin     = "pc"
)

var kindBySubtype = map[string]string{
	"text": "text", "img": "image", "vimg": "image", "vid": "video", "vvid": "video",
	"audio": "audio", "vaud": "audio", "file": "document", "stk": "sticker", "gif": "gif",
	"loc": "location", "cn": "contact", "gi": "invite", "sbm": "system", "tsm": "system",
}

var groupActions = map[string]string{
	"gcj": "joined", "gcl": "left", "gcr": "removed", "gcac": "admin change", "gu": "group updated",
}

var callSubtypes = map[string]string{
	"cinit": "started", "cinv": "ringing", "ciack": "ringing", "cdec": "declined", "ccan": "cancelled",
	"cip": "in progress", "ce": "ended", "csc": "updated", "cbsy": "busy",
}

var stateFromAPI = map[string]string{"READ": "read", "DELIVERED": "delivered", "SENT": "sent", "PENDING": "pending"}

// StateRank orders delivery states; a message's state only ever moves up.
var StateRank = map[string]int{"failed": 0, "pending": 0, "sent": 1, "delivered": 2, "read": 3, "opened": 4}

// Rank returns the StateRank of state, or -1 when it is unknown.
func Rank(state string) int {
	if r, ok := StateRank[state]; ok {
		return r
	}
	return -1
}

var serverIDPattern = regexp.MustCompile(`^([0-9]+):([0-9]+)\n?$`)

// NowMS is the current time in epoch milliseconds.
func NowMS() int64 { return time.Now().UnixMilli() }

// IsServerID reports whether id looks like a server id ("seq:timestamp").
func IsServerID(id string) bool { return serverIDPattern.MatchString(id) }

// ServerSeq returns the sequence number of a "seq:timestamp" server id.
func ServerSeq(serverID string) (int64, bool) {
	m := serverIDPattern.FindStringSubmatch(serverID)
	if m == nil {
		return 0, false
	}
	seq, err := strconv.ParseInt(m[1], 10, 64)
	return seq, err == nil
}

// AsStr turns a JSON value into text. "" stands for a missing or empty value,
// integral floats print as integers (1001.0 -> "1001") and other values like pyjson.Str.
func AsStr(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case float64:
		if !math.IsInf(x, 0) && x == math.Trunc(x) {
			return pyjson.IntString(x)
		}
	}
	return pyjson.Str(v)
}

// AsFloat reads a JSON number, boolean or numeric text.
func AsFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	case int64:
		return float64(x), true
	case int:
		return float64(x), true
	case float64:
		return x, true
	case json.Number:
		f, err := strconv.ParseFloat(string(x), 64)
		return f, err == nil
	case string:
		s := pyjson.Strip(x)
		if s == "" || strings.ContainsAny(s, "xX") { // Go would also accept hex floats
			return 0, false
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil && !isRangeError(err) {
			return 0, false
		}
		return f, true
	}
	return 0, false
}

func isRangeError(err error) bool {
	ne, ok := err.(*strconv.NumError)
	return ok && ne.Err == strconv.ErrRange
}

// AsInt is AsFloat truncated toward zero.
func AsInt(v any) (int64, bool) {
	f, ok := AsFloat(v)
	if !ok || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	f = math.Trunc(f)
	if f < math.MinInt64 || f >= math.MaxInt64 {
		return 0, false
	}
	return int64(f), true
}

// AsIntOr returns AsInt(v), or fallback when v is not a number or is zero.
func AsIntOr(v any, fallback int64) int64 {
	if n, ok := AsInt(v); ok && n != 0 {
		return n
	}
	return fallback
}

func stripUserPrefix(v string) string { return strings.TrimPrefix(v, "u:") }

// ConversationID applies Hike Web's rule: incoming 1:1 -> sender, everything
// else -> `to` (peer or group). ok is false when there is no conversation.
func ConversationID(packet pyjson.Object, me string) (id string, ok bool) {
	sender := AsStr(packet.Get("f"))
	target := AsStr(packet.Get("to"))
	targetOK := target != ""
	target = stripUserPrefix(target)
	if sender == me && target == me {
		return "", false
	}
	if target == me {
		return sender, sender != ""
	}
	return target, targetOK
}

// ---- outgoing packets ------------------------------------------------------

// Reply describes the message being replied to.
type Reply struct {
	ID       string
	SenderID string
	Subtype  any // the stored subtype, or nil
	Text     string
}

func withReply(packet pyjson.Object, reply *Reply) pyjson.Object {
	if reply == nil {
		return packet
	}
	packet.Set("rd", pyjson.O("s", reply.ID, "f", reply.SenderID, "st", reply.Subtype, "pt", reply.Text, "tn", nil))
	return packet
}

// BuildText is the packet Hike Web sends for a text message.
func BuildText(me, conv, text string, reply *Reply) pyjson.Object {
	cid, ts := uuid.New(), NowMS()
	data := pyjson.O("st", STText, "ts", ts, "i", cid, "m", text, "p", text)
	return withReply(pyjson.O("t", TMessage, "st", STText, "c", conv, "f", me, "i", cid, "ts", ts, "to", conv, "d", data), reply)
}

// File describes an uploaded file to send.
type File struct {
	ID, Name, MIME, Caption string
	Size                    int64
}

// BuildFile is the packet Hike Web sends for a document.
func BuildFile(me, conv string, file File, reply *Reply) pyjson.Object {
	cid, ts := uuid.New(), NowMS()
	data := pyjson.O("st", STFile, "ts", ts, "i", cid, "fid", file.ID, "p", file.Name, "s", file.Size, "mime", file.MIME, "fn", file.Name)
	if file.Caption != "" {
		data.Set("m", file.Caption)
	}
	packet := pyjson.O("t", TMessage, "st", STFile, "f", me, "i", cid, "ts", ts, "c", conv, "seq", ts, "node", "default", "to", conv, "d", data)
	return withReply(packet, reply)
}

// BuildMarkRead marks a chat read up to serverID (sent by sender).
func BuildMarkRead(me, conv, sender, serverID string) pyjson.Object {
	return pyjson.O("t", TMarkRead, "f", me, "to", conv, "i", uuid.New(), "d", pyjson.O(sender, serverID), "ts", NowMS())
}

// BuildFirstMarkRead is what Hike Web sends for a chat the API reported unread.
func BuildFirstMarkRead(me, conv, serverID string) pyjson.Object {
	return pyjson.O("t", TFirstMarkRead, "f", me, "to", conv, "d", pyjson.O("s", serverID), "ts", NowMS())
}

// BuildFirstDelivery tells a sender their messages reached this device.
func BuildFirstDelivery(me, conv, serverID string) pyjson.Object {
	return pyjson.O("t", TFirstDelivery, "f", me, "to", conv, "d", pyjson.O("s", serverID), "ts", NowMS())
}

// BuildPresence is the foreground (open tab) or background presence packet.
func BuildPresence(me string, foreground bool) pyjson.Object {
	t := TBackground
	if foreground {
		t = TForeground
	}
	return pyjson.O("t", t, "f", me, "i", uuid.New(), "ts", NowMS())
}

// ---- incoming packets ------------------------------------------------------

// Reaction is one person's reaction to a message.
type Reaction struct {
	Sender, Emoji string
}

// Message is a parsed message-like packet. Empty strings and nil pointers stand
// for missing values (NULL in the database).
type Message struct {
	ChatID        string
	SenderID      string // "" when the packet has no sender (never None)
	FromMe        bool
	TS            int64
	Kind          string
	Subtype       string
	ServerID      string
	ClientID      string
	Text          string
	FileID        string
	FileName      string
	MIME          string
	FileSize      *int64
	Duration      *int64
	Width         *int64
	Height        *int64
	Latitude      *float64
	Longitude     *float64
	Address       string
	ContactName   string
	ContactPhone  string
	ContactUserID string
	ReplyToID     string
	ReplyToSender string
	ReplyToText   string
	Forwarded     bool
	SenderName    string
	SenderPhone   string
	State         string
	// nil means unknown, so a live packet never clears flags learned from history.
	Edited, DeletedForMe, DeletedForAll, Starred, Pinned *bool
	Reactions                                            []Reaction
	Raw                                                  string
}

// ID is the server id, else the client id.
func (m *Message) ID() string {
	if m.ServerID != "" {
		return m.ServerID
	}
	return m.ClientID
}

// IsDeletedForAll reports whether the message is known to be deleted for everyone.
func (m *Message) IsDeletedForAll() bool { return m.DeletedForAll != nil && *m.DeletedForAll }

// PacketFromHistoryItem unwraps a msg-history item into its packet.
func PacketFromHistoryItem(item any) pyjson.Object {
	obj, ok := item.(pyjson.Object)
	if !ok {
		return nil
	}
	raw, ok := obj.Get("messageJson").(string)
	if !ok {
		return obj
	}
	v, err := pyjson.Loads([]byte(raw))
	if err != nil {
		return nil
	}
	packet, _ := v.(pyjson.Object)
	return packet
}

func intPtr(v any) *int64 {
	if n, ok := AsInt(v); ok {
		return &n
	}
	return nil
}

func floatPtr(v any) *float64 {
	if f, ok := AsFloat(v); ok {
		return &f
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// floorDivMod divides rounding toward negative infinity, like Python's divmod.
func floorDivMod(a, b int64) (int64, int64) {
	q, r := a/b, a%b
	if r != 0 && (r < 0) != (b < 0) {
		q, r = q-1, r+b
	}
	return q, r
}

func callText(data pyjson.Object) string {
	stage := "call"
	if st := data.Get("st"); pyjson.Truthy(st) {
		stage = pyjson.Str(st)
		if label, ok := callSubtypes[stage]; ok {
			stage = label
		}
	}
	media := pyjson.Or(data.Get("mt"), data.Get("ct"))
	parts := []string{"call", stage}
	if pyjson.Truthy(media) {
		parts[0] = pyjson.Str(media) + " call"
	}
	if seconds, ok := AsInt(data.Get("dur")); ok && seconds != 0 {
		m, s := floorDivMod(seconds, 60)
		parts = append(parts, fmt.Sprintf("%dm %ds", m, s))
	}
	return strings.Join(parts, " ")
}

func groupActionText(packet, data pyjson.Object) string {
	sub := pyjson.Or(pyjson.Or(packet.Get("st"), data.Get("st")), "")
	label, ok := groupActions[pyjson.Str(sub)]
	if !ok {
		label = pyjson.Str(pyjson.Or(sub, "group update"))
	}
	var names []string
	if members, ok := pyjson.Or(data.Get("nm"), data.Get("m")).([]any); ok {
		for _, m := range members {
			if obj, ok := m.(pyjson.Object); ok && pyjson.Truthy(obj.Get("n")) {
				names = append(names, pyjson.Str(obj.Get("n")))
			}
		}
	}
	if gn := data.Get("gn"); pyjson.Truthy(gn) && sub == "gu" {
		return "group renamed to " + pyjson.Str(gn)
	}
	if len(names) > 0 {
		return label + ": " + strings.Join(names, ", ")
	}
	return label
}

func inviteText(data pyjson.Object) string {
	name := ""
	if gn := data.Get("gn"); pyjson.Truthy(gn) {
		name = pyjson.Str(gn)
	}
	return pyjson.Strip("group invite: " + name)
}

// ParseMessage turns a message-like packet (m, inv, v, gca) into a Message, or
// returns nil for anything else. meta is the msg-history item (or nil) and
// chatID overrides the conversation when known.
func ParseMessage(p pyjson.Object, me string, meta pyjson.Object, chatID string) *Message {
	if p == nil {
		return nil
	}
	ptype := p.Get("t")
	if ptype != TMessage && ptype != TInvite && ptype != TCall && ptype != TGroupAction {
		return nil
	}
	data := p.Obj("d")
	sender := AsStr(p.Get("f"))
	chat := chatID
	if ptype == TGroupAction {
		chat = firstNonEmpty(chatID, AsStr(p.Get("c")), AsStr(data.Get("gid")), stripUserPrefix(AsStr(p.Get("to"))), sender)
	} else if chat == "" {
		chat, _ = ConversationID(p, me)
	}
	if chat == "" {
		return nil
	}
	ts := AsIntOr(p.Get("ts"), AsIntOr(data.Get("ts"), 0))
	subtype := firstNonEmpty(AsStr(p.Get("st")), AsStr(data.Get("st")))
	msg := &Message{
		ChatID:   chat,
		SenderID: sender,
		FromMe:   sender == me,
		TS:       ts,
		Kind:     "text",
		Subtype:  subtype,
		ServerID: firstNonEmpty(AsStr(p.Get("s")), AsStr(p.Get("sid"))),
		ClientID: AsStr(p.Get("i")),
	}
	profile := p.Obj("pi")
	msg.SenderName = AsStr(profile.Get("n"))
	msg.SenderPhone = AsStr(profile.Get("p"))

	switch ptype {
	case TCall:
		msg.Kind = "call"
		if room := AsStr(data.Get("rid")); room != "" {
			msg.ClientID = "call-" + room
		}
		msg.Text = callText(data)
		msg.Duration = intPtr(data.Get("dur"))
	case TGroupAction:
		msg.Kind = "system"
		msg.Text = groupActionText(p, data)
		if msg.ServerID == "" && msg.ClientID == "" {
			msg.ClientID = fmt.Sprintf("gca-%d-%s", ts, subtype)
		}
	case TInvite:
		if subtype != "" && subtype != STGroupInvite {
			return nil
		}
		msg.Kind = "invite"
		msg.Text = inviteText(data)
	default:
		parseContent(msg, data)
	}

	if reply := p.Obj("rd"); len(reply) > 0 {
		msg.ReplyToID = AsStr(reply.Get("s"))
		msg.ReplyToSender = AsStr(reply.Get("f"))
		msg.ReplyToText = AsStr(reply.Get("pt"))
	}
	_, msg.Forwarded = p.Get("fwd").(pyjson.Object)

	if state, ok := meta.Get("state").(string); ok && stateFromAPI[state] != "" {
		msg.State = stateFromAPI[state]
	} else if msg.FromMe {
		msg.State = "pending"
		if msg.ServerID != "" {
			msg.State = "sent"
		}
	} else {
		msg.State = "delivered"
	}
	for _, flag := range []struct {
		dst **bool
		key string
	}{{&msg.Edited, "edited"}, {&msg.DeletedForMe, "deletedForMe"}, {&msg.DeletedForAll, "deletedForAll"}, {&msg.Starred, "starred"}, {&msg.Pinned, "pinned"}} {
		if v := meta.Get(flag.key); v != nil {
			b := pyjson.Truthy(v)
			*flag.dst = &b
		}
	}
	if msg.IsDeletedForAll() {
		msg.Text = ""
	}
	if reactions := meta.Obj("reactionMap"); reactions != nil {
		for _, r := range reactions {
			if pyjson.Truthy(r.Value) {
				msg.Reactions = append(msg.Reactions, Reaction{Sender: r.Key, Emoji: pyjson.Str(r.Value)})
			}
		}
	}
	msg.Raw = pyjson.DumpsCompact(p)
	return msg
}

func parseContent(msg *Message, data pyjson.Object) {
	kind, ok := kindBySubtype[firstNonEmpty(msg.Subtype, "text")]
	if !ok {
		kind = "text"
	}
	msg.Kind = kind
	if text, ok := data.Get("m").(string); ok {
		msg.Text = text
	}
	switch kind {
	case "image", "video", "gif":
		msg.FileID = firstNonEmpty(AsStr(data.Get("fid")), AsStr(data.Get("p")))
	case "audio", "document", "contact":
		msg.FileID = AsStr(data.Get("fid"))
	}
	msg.FileName = AsStr(data.Get("fn"))
	msg.MIME = AsStr(data.Get("mime"))
	msg.FileSize = intPtr(data.Get("s"))
	msg.Duration = intPtr(data.Get("dur"))
	msg.Width = intPtr(data.Get("w"))
	msg.Height = intPtr(data.Get("h"))
	switch kind {
	case "location":
		msg.Latitude = floatPtr(data.Get("lat"))
		msg.Longitude = floatPtr(data.Get("lng"))
		msg.Address = AsStr(data.Get("add"))
		msg.Text = firstNonEmpty(msg.Text, msg.Address)
	case "contact":
		msg.ContactName = firstNonEmpty(AsStr(data.Get("un")), msg.Text)
		var phone []string
		for _, part := range []string{AsStr(data.Get("cod")), AsStr(data.Get("cn"))} {
			if part != "" {
				phone = append(phone, part)
			}
		}
		msg.ContactPhone = strings.Join(phone, " ")
		msg.ContactUserID = AsStr(data.Get("cid"))
	case "invite":
		msg.Text = inviteText(data)
	}
}

var kindLabels = map[string]string{
	"image": "[image]", "video": "[video]", "audio": "[audio]", "document": "[file]", "sticker": "[sticker]",
	"gif": "[gif]", "location": "[location]", "contact": "[contact]", "invite": "[invite]", "call": "[call]",
	"system": "[info]",
}

// Summary is a one-line preview, like Hike Web's chat list.
func Summary(kind, text, fileName string, deleted bool) string {
	if deleted {
		return "This message was deleted"
	}
	label, ok := kindLabels[kind]
	if !ok {
		return text
	}
	bits := []string{label}
	if fileName != "" && (kind == "document" || kind == "audio") {
		bits = append(bits, fileName)
	}
	if text != "" {
		bits = append(bits, text)
	}
	return strings.Join(bits, " ")
}
