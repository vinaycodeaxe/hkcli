// Package syncer keeps the local store up to date: history catch-up over REST
// and live updates over MQTT.
package syncer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/vinaycodeaxe/hkcli/internal/api"
	"github.com/vinaycodeaxe/hkcli/internal/config"
	"github.com/vinaycodeaxe/hkcli/internal/db"
	"github.com/vinaycodeaxe/hkcli/internal/mqtt"
	"github.com/vinaycodeaxe/hkcli/internal/protocol"
	"github.com/vinaycodeaxe/hkcli/internal/pyjson"
)

// FmtTime formats epoch ms as local "YYYY-MM-DD HH:MM"; 0 gives "".
func FmtTime(ms int64) string {
	if ms == 0 {
		return ""
	}
	return time.UnixMilli(ms).Format("2006-01-02 15:04")
}

// OpenMQTT connects like Hike Web: client id web_<userId>_<ms>, user name =
// user id, password = access token. A refused login refreshes the token once.
func OpenMQTT(ctx context.Context, client *api.Client) (*mqtt.Client, error) {
	for attempt := 0; ; attempt++ {
		session := client.Session
		c := &mqtt.Client{
			URL:       config.MQTTWSURL(),
			ClientID:  fmt.Sprintf("web_%s_%d", session.UserID, protocol.NowMS()),
			Username:  session.UserID,
			Password:  session.AccessToken,
			Keepalive: 25 * time.Second,
			Origin:    config.WebOrigin,
			UserAgent: config.UserAgent(),
		}
		err := c.Connect(ctx)
		if err == nil {
			return c, nil
		}
		if !mqtt.IsKind(err, mqtt.KindAuth) {
			return nil, err
		}
		if attempt > 0 {
			return nil, &api.AuthRequiredError{Msg: "Hike's message relay rejected the login even after a token refresh. Run: hkcli auth"}
		}
		if err := client.Refresh(ctx); err != nil {
			return nil, err
		}
	}
}

// PublishPacket publishes to the user's own topic (as Hike Web does) and
// returns the server id from the PUBACK ("" if none). QoS 1 publishes are
// retried with the same payload, like Hike Web.
func PublishPacket(ctx context.Context, client *mqtt.Client, me string, packet pyjson.Object, qos byte) (string, error) {
	payload := []byte(pyjson.DumpsCompact(packet))
	if qos == 0 {
		_, err := client.Publish(ctx, me, payload, 0, 0)
		return "", err
	}
	const attempts = 3
	for attempt := 0; ; attempt++ {
		ack, err := client.Publish(ctx, me, payload, 1, 5*time.Second)
		if err == nil {
			return string([]rune(string(ack))), nil // invalid UTF-8 becomes U+FFFD
		}
		if !mqtt.IsKind(err, mqtt.KindTimeout) {
			return "", err
		}
		if attempt == attempts-1 {
			return "", &mqtt.Error{Kind: mqtt.KindTimeout, Msg: fmt.Sprintf("Hike did not confirm delivery after %d attempts; the message may or may not have been sent", attempts)}
		}
		if err := wait(ctx, min(4*time.Second, time.Second<<attempt)); err != nil {
			return "", err
		}
	}
}

func wait(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// Emitter writes human lines (stdout for messages, stderr for status) or NDJSON
// events on stdout.
type Emitter struct {
	JSON, Quiet bool
	Out, Err    io.Writer
}

// NewEmitter writes to stdout and stderr.
func NewEmitter(jsonMode, quiet bool) *Emitter {
	return &Emitter{JSON: jsonMode, Quiet: quiet, Out: os.Stdout, Err: os.Stderr}
}

// Emit prints an NDJSON event (JSON mode only) with alternating keys and values.
func (e *Emitter) Emit(event string, kv ...any) {
	if e.JSON && !e.Quiet {
		fmt.Fprintln(e.Out, pyjson.Dumps(append(pyjson.O("event", event), pyjson.O(kv...)...)))
	}
}

// Line prints a human line, or the event in JSON mode.
func (e *Emitter) Line(human, event string, kv ...any) {
	if e.JSON {
		e.Emit(event, kv...)
	} else if !e.Quiet {
		fmt.Fprintln(e.Out, human)
	}
}

// Status reports progress: a status event in JSON mode, else a line on stderr.
func (e *Emitter) Status(text string) {
	if e.JSON {
		e.Emit("status", "message", text)
	} else {
		fmt.Fprintln(e.Err, text)
	}
}

// Options tune a Syncer.
type Options struct {
	Receipts  bool   // send delivery receipts for messages fetched while offline
	Presence  string // "quiet" (background tab) or "normal" (open tab)
	MaxChats  int    // chats to fetch per catch-up (0 = all)
	BatchSize int    // messages per history request
	MaxPages  int    // history pages per chat when catching up
}

// DefaultOptions match the sync command's defaults.
func DefaultOptions() Options {
	return Options{Receipts: true, Presence: "quiet", MaxChats: 50, BatchSize: 50, MaxPages: 3}
}

// Syncer copies chats, messages and contacts from Hike into the store.
type Syncer struct {
	conn          *db.Conn
	api           *api.Client
	me            string
	out           *Emitter
	opts          Options
	client        *mqtt.Client
	groupsFetched map[string]bool
}

// New prepares a Syncer for the logged-in user me.
func New(conn *db.Conn, client *api.Client, me string, out *Emitter, opts Options) *Syncer {
	opts.MaxPages = max(1, opts.MaxPages)
	return &Syncer{conn: conn, api: client, me: me, out: out, opts: opts, groupsFetched: map[string]bool{}}
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func boolPtr(b bool) *bool { return &b }

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func objects(list []any) []pyjson.Object {
	out := make([]pyjson.Object, 0, len(list))
	for _, item := range list {
		if obj, ok := item.(pyjson.Object); ok {
			out = append(out, obj)
		}
	}
	return out
}

func isAuthRequired(err error) bool {
	var ar *api.AuthRequiredError
	return errors.As(err, &ar)
}

// ---- REST ----------------------------------------------------------------

// SyncContacts downloads contacts changed since the last sync.
func (s *Syncer) SyncContacts(ctx context.Context) (int, error) {
	meta, _, err := s.conn.GetMeta("contacts_synced_at")
	if err != nil {
		return 0, err
	}
	syncedAt, _ := strconv.ParseInt(strings.TrimSpace(meta), 10, 64)
	var cursor any
	count, latest := 0, syncedAt
	for {
		contacts, next, serverTime, err := s.api.ContactsPage(ctx, syncedAt, cursor)
		if err != nil {
			return count, err
		}
		for _, c := range objects(contacts) {
			uid := protocol.AsStr(c.Get("userId"))
			if uid == "" {
				continue
			}
			ct := db.Contact{InAddressBook: boolPtr(false)}
			if !pyjson.Truthy(c.Get("isDeleted")) {
				ct = db.Contact{
					Name:          protocol.AsStr(c.Get("nickName")),
					Phone:         protocol.AsStr(c.Get("phoneNumber")),
					Status:        protocol.AsStr(c.Get("status")),
					Pic:           protocol.AsStr(c.Get("pic")),
					InAddressBook: boolPtr(true),
				}
			}
			if err := s.conn.UpsertContact(uid, ct, true); err != nil {
				return count, err
			}
			count++
		}
		latest = protocol.AsIntOr(serverTime, latest)
		if cursor = next; !pyjson.Truthy(cursor) {
			break
		}
	}
	if err := s.conn.SetMeta("contacts_synced_at", strconv.FormatInt(latest, 10)); err != nil {
		return count, err
	}
	return count, s.conn.Commit()
}

type chatInfo struct {
	id           string
	lastTS       int64
	lastSender   any // string or nil, as stored
	lastServerID string
	undelivered  bool
}

// syncConversations stores Hike's chat list and returns what catch-up needs.
func (s *Syncer) syncConversations(ctx context.Context) ([]chatInfo, error) {
	items, err := s.api.Conversations(ctx)
	if err != nil {
		return nil, err
	}
	var chats []chatInfo
	for _, item := range objects(items) {
		cid := protocol.AsStr(item.Get("conversationId"))
		if cid == "" || cid == s.me {
			continue
		}
		chatType := "group"
		if item.Get("conversationType") == "ONE_ON_ONE" {
			chatType = "one_to_one"
		}
		phone := protocol.AsStr(item.Get("phoneNumber"))
		name := protocol.AsStr(item.Get("conversationName"))
		if name == "" {
			name = phone
		}
		unread := protocol.AsIntOr(item.Get("unreadCount"), 0)
		undelivered := pyjson.Truthy(item.Get("undelivered"))
		fields := []any{
			"type", chatType, "unread", unread, "api_unread", boolInt(unread > 0), "undelivered", boolInt(undelivered),
			"archived", boolInt(pyjson.Truthy(item.Get("archived"))), "muted", boolInt(pyjson.Truthy(item.Get("muted"))),
			"blocked", boolInt(pyjson.Truthy(item.Get("blocked"))), "pinned", boolInt(pyjson.Truthy(item.Get("pinned"))),
		}
		if name != "" {
			fields = append(fields, "name", name)
		}
		if phone != "" {
			fields = append(fields, "phone", phone)
		}
		lastTS, _ := protocol.AsInt(item.Get("lastMessageTs"))
		lastSender := nullable(protocol.AsStr(item.Get("lastMessageSentBy")))
		lastServerID := protocol.AsStr(item.Get("lastMessageId"))
		packet := item.Get("lastMessage")
		if text, ok := packet.(string); ok {
			packet, _ = pyjson.Loads([]byte(text))
		}
		if p, ok := packet.(pyjson.Object); ok {
			if msg := protocol.ParseMessage(p, s.me, nil, cid); msg != nil {
				deleted := pyjson.Truthy(item.Get("lastMessageIsDeletedForAll"))
				text := msg.Text
				if deleted {
					text = ""
				}
				fields = append(fields, "last_text", protocol.Summary(msg.Kind, text, msg.FileName, deleted))
				if lastServerID == "" {
					lastServerID = msg.ServerID
				}
				lastSender = pyjson.Or(lastSender, msg.SenderID)
			}
		}
		if _, ok := protocol.ServerSeq(lastServerID); !ok {
			lastServerID = ""
		}
		if lastTS != 0 {
			fields = append(fields, "last_ts", lastTS, "last_sender", lastSender)
			if lastServerID != "" {
				fields = append(fields, "last_server_id", lastServerID)
			}
		}
		if err := s.conn.UpdateChat(cid, fields...); err != nil {
			return nil, err
		}
		if chatType == "one_to_one" && (name != "" || phone != "") {
			contactName := name
			if name == phone {
				contactName = ""
			}
			if err := s.conn.UpsertContact(cid, db.Contact{Name: contactName, Phone: phone}, false); err != nil {
				return nil, err
			}
		}
		chats = append(chats, chatInfo{id: cid, lastTS: lastTS, lastSender: lastSender, lastServerID: lastServerID, undelivered: undelivered})
	}
	return chats, s.conn.Commit()
}

func (s *Syncer) learnSender(msg *protocol.Message) error {
	if !msg.FromMe && msg.SenderID != "" && (msg.SenderName != "" || msg.SenderPhone != "") {
		return s.conn.UpsertContact(msg.SenderID, db.Contact{Name: msg.SenderName, Phone: msg.SenderPhone}, false)
	}
	return nil
}

func (s *Syncer) storeHistory(chatID string, items []any) (added, known int, err error) {
	for _, item := range items {
		packet := protocol.PacketFromHistoryItem(item)
		if len(packet) == 0 {
			continue
		}
		meta, _ := item.(pyjson.Object)
		msg := protocol.ParseMessage(packet, s.me, meta, chatID)
		if msg == nil {
			continue
		}
		_, inserted, err := s.conn.UpsertMessage(msg)
		if err == nil {
			err = s.learnSender(msg)
		}
		if err == nil {
			err = s.conn.TouchChatLast(msg)
		}
		if err != nil {
			return added, known, err
		}
		if inserted {
			added++
		} else {
			known++
		}
	}
	return added, known, nil
}

func (s *Syncer) initialized(chatID string) (bool, error) {
	row, err := s.conn.GetChat(chatID)
	if err != nil {
		return false, err
	}
	if row != nil && (row.Truthy("history_cursor") || row.Truthy("history_done")) {
		return true, nil
	}
	_, ok, err := s.conn.ChatLastLocalTS(chatID)
	return ok, err
}

func isNone(v any) int {
	if v == nil {
		return 1
	}
	return 0
}

// catchUpChat fetches the newest pages until they overlap what is stored. A
// chat seen for the first time gets one page and a cursor for backfill.
func (s *Syncer) catchUpChat(ctx context.Context, chatID string) (int, error) {
	initialized, err := s.initialized(chatID)
	if err != nil {
		return 0, err
	}
	pages := s.opts.MaxPages
	if !initialized {
		pages = 1
	}
	var cursor any
	total := 0
	for range pages {
		items, next, err := s.api.Messages(ctx, chatID, s.opts.BatchSize, cursor)
		if err != nil {
			return total, err
		}
		added, known, err := s.storeHistory(chatID, items)
		total += added
		if err == nil && !initialized {
			err = s.conn.UpdateChat(chatID, "history_cursor", next, "history_done", isNone(next))
		}
		if err == nil {
			err = s.conn.Commit()
		}
		if err != nil {
			return total, err
		}
		if known > 0 || !pyjson.Truthy(next) {
			break
		}
		cursor = next
	}
	return total, nil
}

// BackfillChat walks older history from the saved cursor. It returns the new
// messages and whether the start of the chat was reached.
func (s *Syncer) BackfillChat(ctx context.Context, chatID string, pages int) (int, bool, error) {
	row, err := s.conn.GetChat(chatID)
	if err != nil {
		return 0, false, err
	}
	if row != nil && row.Truthy("history_done") {
		return 0, true, nil
	}
	var cursor any
	if row != nil {
		cursor = row["history_cursor"]
	}
	total := 0
	for range max(1, pages) {
		items, next, err := s.api.Messages(ctx, chatID, s.opts.BatchSize, cursor)
		if err != nil {
			return total, false, err
		}
		added, _, err := s.storeHistory(chatID, items)
		total += added
		if err == nil {
			err = s.conn.UpdateChat(chatID, "history_cursor", next, "history_done", isNone(next))
		}
		if err == nil {
			err = s.conn.Commit()
		}
		if err != nil {
			return total, false, err
		}
		if !pyjson.Truthy(next) {
			return total, true, nil
		}
		cursor = next
	}
	return total, false, nil
}

// ResolveNames looks up names for senders and chats that only have an id.
func (s *Syncer) ResolveNames(ctx context.Context) (int, error) {
	ids, err := s.conn.UnnamedSenders(s.me, 200)
	if err != nil {
		return 0, err
	}
	chats, err := s.conn.UnnamedDirectChats()
	if err != nil {
		return 0, err
	}
	seen := map[string]bool{}
	for _, id := range ids {
		seen[id] = true
	}
	for _, id := range chats {
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return 0, nil
	}
	profiles, err := s.api.Profiles(ctx, ids[:min(len(ids), 200)])
	if err != nil {
		if isAuthRequired(err) || !api.IsHikeError(err) {
			return 0, err
		}
		s.out.Status(fmt.Sprintf("could not look up %d profile names: %v", len(ids), err))
		return 0, nil
	}
	for _, profile := range objects(profiles) {
		uid := protocol.AsStr(profile.Get("userId"))
		if uid == "" {
			continue
		}
		name, phone := protocol.AsStr(profile.Get("name")), protocol.AsStr(profile.Get("phone"))
		ct := db.Contact{Name: name, Phone: phone, Status: protocol.AsStr(profile.Get("statusMsg")), Pic: protocol.AsStr(profile.Get("image"))}
		if err := s.conn.UpsertContact(uid, ct, false); err != nil {
			return 0, err
		}
		chat, err := s.conn.GetChat(uid)
		if err != nil {
			return 0, err
		}
		if chat != nil && (!chat.Truthy("name") || chat.Str("name") == uid) && (name != "" || phone != "") {
			if name == "" {
				name = phone
			}
			if err := s.conn.UpdateChat(uid, "name", name); err != nil {
				return 0, err
			}
		}
	}
	return len(profiles), s.conn.Commit()
}

func (s *Syncer) isStale(chat chatInfo) (bool, error) {
	if chat.lastTS == 0 {
		return false, nil
	}
	if chat.lastServerID != "" {
		row, err := s.conn.FindMessage(chat.id, chat.lastServerID)
		if err != nil || row != nil {
			return false, err
		}
	}
	local, ok, err := s.conn.ChatLastLocalTS(chat.id)
	return !ok || chat.lastTS > local, err
}

// Counts summarizes a catch-up.
type Counts struct {
	Contacts, Chats, ChatsFetched, Messages, Receipts int
}

// Pairs lists the counts as alternating keys and values, in output order.
func (c Counts) Pairs() []any {
	return []any{"contacts", c.Contacts, "chats", c.Chats, "chats_fetched", c.ChatsFetched, "messages", c.Messages, "receipts", c.Receipts}
}

// CatchUp syncs contacts (optionally), the chat list, stale chats and names,
// then sends delivery receipts when connected to the relay.
func (s *Syncer) CatchUp(ctx context.Context, contacts bool) (Counts, error) {
	var counts Counts
	if contacts {
		n, err := s.SyncContacts(ctx)
		switch {
		case err == nil:
			counts.Contacts = n
		case isAuthRequired(err) || !api.IsHikeError(err):
			return counts, err
		default:
			s.out.Status(fmt.Sprintf("contacts sync failed: %v", err))
		}
	}
	chats, err := s.syncConversations(ctx)
	if err != nil {
		return counts, err
	}
	counts.Chats = len(chats)
	var stale []chatInfo
	for _, chat := range chats {
		ok, err := s.isStale(chat)
		if err != nil {
			return counts, err
		}
		if ok {
			stale = append(stale, chat)
		}
	}
	sort.SliceStable(stale, func(i, j int) bool { return stale[i].lastTS > stale[j].lastTS })
	skipped := 0
	if limit := s.opts.MaxChats; limit != 0 && len(stale) > limit {
		skipped = len(stale) - limit
		if limit < 0 { // like a negative slice index: drop that many from the end
			limit = max(0, len(stale)+limit)
		}
		stale = stale[:limit]
	}
	for _, chat := range stale {
		n, err := s.catchUpChat(ctx, chat.id)
		counts.Messages += n
		if err != nil {
			return counts, err
		}
		counts.ChatsFetched++
	}
	if skipped > 0 {
		s.out.Status(fmt.Sprintf("%d older chats were not fetched this time (use --max-chats 0, or 'hkcli history backfill --chat ...')", skipped))
	}
	if _, err := s.ResolveNames(ctx); err != nil {
		return counts, err
	}
	if s.opts.Receipts && s.client != nil {
		n, err := s.sendFirstDeliveries(ctx, chats)
		if err != nil {
			return counts, err
		}
		counts.Receipts = n
	}
	s.out.Emit("sync", counts.Pairs()...)
	return counts, nil
}

// sendFirstDeliveries tells senders their messages reached this device (Hike
// Web does this on load).
func (s *Syncer) sendFirstDeliveries(ctx context.Context, chats []chatInfo) (int, error) {
	sent := 0
	for _, chat := range chats {
		if !chat.undelivered || chat.lastServerID == "" || !pyjson.Truthy(chat.lastSender) || chat.lastSender == s.me {
			continue
		}
		_, err := PublishPacket(ctx, s.client, s.me, protocol.BuildFirstDelivery(s.me, chat.id, chat.lastServerID), 1)
		var relayErr *mqtt.Error
		if errors.As(err, &relayErr) {
			s.out.Status(fmt.Sprintf("could not send delivery receipts: %v", err))
			break
		}
		if err != nil {
			return sent, err
		}
		if err := s.conn.UpdateChat(chat.id, "undelivered", 0); err != nil {
			return sent, err
		}
		sent++
	}
	return sent, s.conn.Commit()
}

// ---- live ------------------------------------------------------------------

// handlePacket stores one live packet and reports it.
func (s *Syncer) handlePacket(ctx context.Context, packet pyjson.Object) error {
	var err error
	switch kind := packet.Get("t"); kind {
	case protocol.TMessage, protocol.TInvite, protocol.TCall, protocol.TGroupAction:
		err = s.onMessage(ctx, packet)
	case protocol.TDelivery, protocol.TFirstDelivery:
		err = s.onReceipt(packet, "delivered")
	case protocol.TMarkRead, protocol.TFirstMarkRead:
		err = s.onReceipt(packet, "read")
	case protocol.TReaction:
		err = s.onReaction(packet)
	case protocol.TMessageAction:
		err = s.onMessageAction(packet)
	case protocol.TConversationAction:
		err = s.onConversationAction(packet)
	case protocol.TStartTyping, protocol.TEndTyping:
		if sender := protocol.AsStr(packet.Get("f")); sender != s.me {
			var chat any
			if id, ok := protocol.ConversationID(packet, s.me); ok {
				chat = id
			}
			s.out.Emit("typing", "chat", chat, "user", nullable(sender), "typing", kind == protocol.TStartTyping)
		}
	case protocol.TPresence:
		data := packet.Obj("d")
		var lastSeen any
		if n, ok := protocol.AsInt(data.Get("pr")); ok {
			lastSeen = n
		}
		s.out.Emit("presence", "user", nullable(protocol.AsStr(data.Get("uid"))), "last_seen", lastSeen)
	case protocol.TDataSync:
		s.out.Emit("data_sync", "kind", packet.Obj("d").Get("st"))
	default:
		s.out.Emit("packet", "type", kind)
	}
	if err != nil {
		return err
	}
	return s.conn.Commit()
}

func (s *Syncer) chatName(chatID string) (string, error) {
	row, err := s.conn.GetChat(chatID)
	if err != nil {
		return "", err
	}
	if row != nil && row.Truthy("name") {
		return row.Str("name"), nil
	}
	return s.conn.DisplayName(chatID, "")
}

func (s *Syncer) fetchGroup(ctx context.Context, groupID string) error {
	if s.groupsFetched[groupID] {
		return nil
	}
	s.groupsFetched[groupID] = true
	info, err := s.api.Group(ctx, groupID)
	if err != nil {
		if isAuthRequired(err) || !api.IsHikeError(err) {
			return err
		}
		return nil
	}
	if name := info.Get("name"); pyjson.Truthy(name) {
		if err := s.conn.UpdateChat(groupID, "type", "group", "name", name); err != nil {
			return err
		}
	}
	members, _ := info.Get("members").([]any)
	return s.conn.ReplaceGroupMembers(groupID, members)
}

func (s *Syncer) onMessage(ctx context.Context, packet pyjson.Object) error {
	msg := protocol.ParseMessage(packet, s.me, nil, "")
	if msg == nil {
		return nil
	}
	target := protocol.AsStr(packet.Get("to"))
	isGroup := packet.Get("t") == protocol.TGroupAction || (!msg.FromMe && target != s.me && target != "u:"+s.me)
	existing, err := s.conn.GetChat(msg.ChatID)
	if err != nil {
		return err
	}
	if existing == nil {
		chatType := "one_to_one"
		if isGroup {
			chatType = "group"
		}
		if err := s.conn.EnsureChat(msg.ChatID, chatType); err != nil {
			return err
		}
		if isGroup {
			err = s.fetchGroup(ctx, msg.ChatID)
		} else if !msg.FromMe && (msg.SenderName != "" || msg.SenderPhone != "") {
			name := msg.SenderName
			if name == "" {
				name = msg.SenderPhone
			}
			err = s.conn.UpdateChat(msg.ChatID, "name", name, "phone", nullable(msg.SenderPhone))
		}
		if err != nil {
			return err
		}
	}
	_, inserted, err := s.conn.UpsertMessage(msg)
	if err == nil {
		err = s.learnSender(msg)
	}
	if err == nil {
		err = s.conn.TouchChatLast(msg)
	}
	if err == nil && inserted && !msg.FromMe && msg.Kind != "call" && msg.Kind != "system" {
		err = s.conn.IncrementUnread(msg.ChatID)
	}
	if err == nil {
		err = s.conn.Commit()
	}
	if err != nil || !inserted {
		return err
	}
	return s.printMessage(msg)
}

func (s *Syncer) printMessage(msg *protocol.Message) error {
	chat, err := s.conn.GetChat(msg.ChatID)
	if err != nil {
		return err
	}
	chatName, err := s.chatName(msg.ChatID)
	if err != nil {
		return err
	}
	sender := "me"
	if !msg.FromMe {
		if sender, err = s.conn.DisplayName(msg.SenderID, ""); err != nil {
			return err
		}
		if sender == "" {
			sender = msg.SenderName
		}
		if sender == "" {
			sender = msg.SenderID
		}
	}
	text := protocol.Summary(msg.Kind, msg.Text, msg.FileName, msg.IsDeletedForAll())
	chatType := "one_to_one"
	if chat != nil && chat["type"] == "group" {
		chatType = "group"
	}
	mentions := make([]pyjson.Object, 0, len(msg.Mentions))
	mentionsMe := false
	for _, m := range msg.Mentions {
		mentions = append(mentions, pyjson.O("uid", nullable(m.UserID), "phone", nullable(m.Phone), "start", m.Start, "length", m.Length))
		mentionsMe = mentionsMe || m.UserID == s.me
	}
	var line string
	switch {
	case chatType == "group":
		line = fmt.Sprintf("%s  %s / %s: %s", FmtTime(msg.TS), chatName, sender, text)
	case msg.FromMe:
		line = fmt.Sprintf("%s  me -> %s: %s", FmtTime(msg.TS), chatName, text)
	default:
		line = fmt.Sprintf("%s  %s: %s", FmtTime(msg.TS), sender, text)
	}
	s.out.Line(line, "message",
		"chat", msg.ChatID, "chat_name", chatName, "chat_type", chatType, "id", nullable(msg.ID()), "server_id", nullable(msg.ServerID),
		"client_id", nullable(msg.ClientID), "sender", msg.SenderID, "sender_name", sender, "from_me", msg.FromMe,
		"ts", msg.TS, "kind", msg.Kind, "text", nullable(msg.Text), "file_name", nullable(msg.FileName),
		"reply_to", nullable(msg.ReplyToID), "mentions", mentions, "mentions_me", mentionsMe)
	return nil
}

func (s *Syncer) onReceipt(packet pyjson.Object, state string) error {
	chat, _ := protocol.ConversationID(packet, s.me)
	sender := protocol.AsStr(packet.Get("f"))
	if chat == "" {
		return nil
	}
	if sender == s.me {
		if state == "read" { // read on another of my devices
			return s.conn.UpdateChat(chat, "unread", 0, "api_unread", 0)
		}
		return nil
	}
	var serverID string
	switch data := packet.Get("d").(type) {
	case nil:
	case pyjson.Object:
		serverID = protocol.AsStr(data.Get(s.me))
		if serverID == "" {
			serverID = protocol.AsStr(data.Get("s"))
		}
		if serverID == "" && len(data) == 1 {
			serverID = protocol.AsStr(data[0].Value)
		}
	default:
		serverID = protocol.AsStr(data)
	}
	if serverID == "" {
		serverID = protocol.AsStr(packet.Get("s"))
	}
	seq, ok := protocol.ServerSeq(serverID)
	row, err := s.conn.GetChat(chat)
	if err != nil || !ok || (row != nil && row["type"] == "group") {
		return err // group receipts are per member; Hike Web tracks them separately
	}
	updated, err := s.conn.MarkMyMessages(chat, seq, state)
	if err != nil {
		return err
	}
	s.out.Emit("receipt", "chat", chat, "by", nullable(sender), "state", state, "up_to", serverID, "updated", updated)
	return nil
}

func (s *Syncer) onReaction(packet pyjson.Object) error {
	chat, _ := protocol.ConversationID(packet, s.me)
	data := packet.Obj("d")
	target, sender := protocol.AsStr(data.Get("sid")), protocol.AsStr(packet.Get("f"))
	st := data.Get("st")
	if chat == "" || target == "" || sender == "" || (st != protocol.STAddReaction && st != protocol.STRemoveReaction) {
		return nil
	}
	emoji := ""
	if st == protocol.STAddReaction {
		emoji = protocol.AsStr(data.Get("r"))
	}
	ts, _ := protocol.AsInt(packet.Get("ts"))
	if err := s.conn.SetReaction(chat, target, sender, emoji, protocol.AsIntOr(ts, protocol.NowMS())); err != nil {
		return err
	}
	line := ""
	if emoji != "" {
		who := "me"
		if sender != s.me {
			name, err := s.conn.DisplayName(sender, "")
			if err != nil {
				return err
			}
			who = name
		}
		chatName, err := s.chatName(chat)
		if err != nil {
			return err
		}
		line = fmt.Sprintf("%s  %s reacted %s in %s", FmtTime(ts), who, emoji, chatName)
	}
	if line != "" || s.out.JSON {
		s.out.Line(line, "reaction", "chat", chat, "target", target, "user", sender, "emoji", nullable(emoji))
	}
	return nil
}

func (s *Syncer) onMessageAction(packet pyjson.Object) error {
	chat, _ := protocol.ConversationID(packet, s.me)
	data := packet.Obj("d")
	action, target := data.Get("st"), protocol.AsStr(data.Get("sid"))
	if chat == "" || target == "" {
		return nil
	}
	sender := protocol.AsStr(packet.Get("f"))
	changed, err := s.conn.ApplyMessageAction(chat, action, target, data.Get("m"), data.Get("val"), sender == s.me)
	if err != nil {
		return err
	}
	ts, _ := protocol.AsInt(packet.Get("ts"))
	switch action {
	case protocol.MAEdit, protocol.MADeleteForAll:
		chatName, err := s.chatName(chat)
		if err != nil {
			return err
		}
		if action == protocol.MAEdit {
			s.out.Line(fmt.Sprintf("%s  message edited in %s: %s", FmtTime(ts), chatName, pyjson.Str(data.Get("m"))),
				"edit", "chat", chat, "target", target, "text", data.Get("m"), "updated", changed)
		} else {
			s.out.Line(fmt.Sprintf("%s  message deleted in %s", FmtTime(ts), chatName),
				"delete", "chat", chat, "target", target, "updated", changed)
		}
	default:
		s.out.Emit("message_action", "chat", chat, "action", action, "target", target, "value", data.Get("val"), "updated", changed)
	}
	return nil
}

var chatFlagColumns = map[string]string{protocol.CAArchive: "archived", protocol.CAMute: "muted", protocol.CAPin: "pinned", protocol.CABlock: "blocked"}

func (s *Syncer) onConversationAction(packet pyjson.Object) error {
	chat, _ := protocol.ConversationID(packet, s.me)
	data := packet.Obj("d")
	action, value := data.Get("st"), pyjson.Truthy(data.Get("val"))
	if chat == "" {
		return nil
	}
	actionText, _ := action.(string)
	var err error
	switch column := chatFlagColumns[actionText]; {
	case column != "":
		err = s.conn.UpdateChat(chat, column, boolInt(value))
	case action == protocol.CAUnread && value:
		err = s.conn.MarkUnread(chat)
	case action == protocol.CAUnread:
		err = s.conn.UpdateChat(chat, "unread", 0, "api_unread", 0)
	case action == protocol.CAClear || action == protocol.CADelete:
		cutoff := protocol.AsIntOr(data.Get("ts"), protocol.AsIntOr(packet.Get("ts"), protocol.NowMS()))
		err = s.conn.DeleteForMeUpTo(chat, cutoff)
	}
	if err != nil {
		return err
	}
	s.out.Emit("chat_action", "chat", chat, "action", action, "value", value)
	return nil
}

// retryable errors mean the connection or a request failed; follow reconnects.
func retryable(err error) bool {
	var relayErr *mqtt.Error
	var hikeErr *api.Error
	return errors.As(err, &relayErr) || errors.As(err, &hikeErr)
}

// Follow stays connected, stores everything that arrives and reconnects on
// drops (backoff 1 s doubling to 60 s). It returns why it stopped: "idle" after
// idleExit without events, "interrupted" when ctx is cancelled.
func (s *Syncer) Follow(ctx context.Context, idleExit, maxReconnect *time.Duration) (string, error) {
	backoff, first := time.Second, true
	var downSince time.Time
	for ctx.Err() == nil {
		client, err := OpenMQTT(ctx, s.api)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			if !retryable(err) {
				return "", err
			}
			if downSince.IsZero() {
				downSince = time.Now()
			}
			if maxReconnect != nil && time.Since(downSince) >= *maxReconnect {
				return "", err
			}
			s.out.Status(fmt.Sprintf("cannot connect to Hike (%v); retrying in %.0fs", err, backoff.Seconds()))
			_ = wait(ctx, backoff)
			backoff = min(backoff*2, 60*time.Second)
			continue
		}
		s.client, downSince = client, time.Time{}
		reason, err := s.listen(ctx, client, &first, &backoff, idleExit)
		if err != nil && ctx.Err() == nil && retryable(err) {
			s.out.Status(fmt.Sprintf("connection lost (%v); reconnecting in %.0fs", err, backoff.Seconds()))
			s.out.Emit("disconnected", "reason", err.Error())
			_ = wait(ctx, backoff)
			backoff = min(backoff*2, 60*time.Second)
			err = nil
		}
		if client.Connected() {
			_, _ = PublishPacket(context.Background(), client, s.me, protocol.BuildPresence(s.me, false), 0)
		}
		client.Disconnect()
		s.client = nil
		if ctx.Err() != nil {
			break
		}
		if err != nil {
			return "", err
		}
		if reason != "" {
			return reason, nil
		}
	}
	return "interrupted", nil
}

func (s *Syncer) listen(ctx context.Context, client *mqtt.Client, first *bool, backoff *time.Duration, idleExit *time.Duration) (string, error) {
	if _, err := client.Subscribe(ctx, "u:"+s.me, 1); err != nil {
		return "", err
	}
	if _, err := PublishPacket(ctx, client, s.me, protocol.BuildPresence(s.me, s.opts.Presence == "normal"), 0); err != nil {
		return "", err
	}
	s.out.Emit("connected")
	counts, err := s.CatchUp(ctx, *first)
	if err != nil {
		return "", err
	}
	*first, *backoff = false, time.Second
	if !s.out.JSON {
		s.out.Status(fmt.Sprintf("caught up: %d new messages in %d chats", counts.Messages, counts.ChatsFetched))
	}
	s.out.Status("listening for new messages (Ctrl+C to stop)")
	lastActivity := time.Now()
	for {
		item, err := client.Next(ctx, time.Second)
		if err != nil {
			return "", err
		}
		if item == nil {
			if idleExit != nil && time.Since(lastActivity) >= *idleExit {
				return "idle", nil
			}
			continue
		}
		lastActivity = time.Now()
		if !utf8.Valid(item.Payload) {
			continue
		}
		v, err := pyjson.Loads(item.Payload)
		packet, ok := v.(pyjson.Object)
		if err != nil || !ok {
			continue
		}
		if _, ok := packet.Lookup("t"); !ok {
			continue
		}
		if err := s.handlePacket(ctx, packet); err != nil {
			if isAuthRequired(err) || ctx.Err() != nil {
				return "", err
			}
			_ = s.conn.Rollback()
			s.out.Status(fmt.Sprintf("could not process a '%s' packet: %v", pyjson.Str(packet.Get("t")), err))
		}
	}
}
