package cli

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/vinaycodeaxe/hkcli/internal/db"
	"github.com/vinaycodeaxe/hkcli/internal/phone"
	"github.com/vinaycodeaxe/hkcli/internal/protocol"
	"github.com/vinaycodeaxe/hkcli/internal/pyjson"
	"github.com/vinaycodeaxe/hkcli/internal/store"
	"github.com/vinaycodeaxe/hkcli/internal/syncer"
)

// iso formats epoch ms as ISO 8601 UTC, with microseconds unless they are zero
// (2025-10-09T08:53:20.500000+00:00); 0 gives nil.
func iso(ms int64) any {
	if ms == 0 {
		return nil
	}
	t := time.UnixMilli(ms).UTC()
	if t.Nanosecond() != 0 {
		return t.Format("2006-01-02T15:04:05.000000-07:00")
	}
	return t.Format("2006-01-02T15:04:05-07:00")
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// clip shortens a table cell to width characters, ending with "…".
func clip(v any, width int) string {
	text := ""
	if v != nil {
		text = strings.NewReplacer("\r", " ", "\n", " ").Replace(pyjson.Str(v))
	}
	runes := []rune(text)
	if len(runes) <= width {
		return text
	}
	return string(runes[:max(0, width-1)]) + "…"
}

// printTable prints left-aligned columns; widths caps columns (default 60).
func (c *Ctx) printTable(headers []string, rows [][]any, widths map[int]int) {
	width := func(i int) int {
		if w, ok := widths[i]; ok {
			return w
		}
		return 60
	}
	cells := make([][]string, len(rows))
	sizes := make([]int, len(headers))
	for i, h := range headers {
		sizes[i] = len([]rune(h))
	}
	for r, row := range rows {
		cells[r] = make([]string, len(row))
		for i, v := range row {
			cells[r][i] = clip(v, width(i))
			sizes[i] = max(sizes[i], len([]rune(cells[r][i])))
		}
	}
	line := func(values []string) string {
		padded := make([]string, len(values))
		for i, v := range values {
			padded[i] = v + strings.Repeat(" ", sizes[i]-len([]rune(v)))
		}
		return strings.TrimRightFunc(strings.Join(padded, "  "), pyjson.IsSpace)
	}
	c.println(line(headers))
	for _, row := range cells {
		c.println(line(row))
	}
}

// storeLock takes the store's LOCK without waiting.
func (c *Ctx) storeLock(what string) (*store.FileLock, error) {
	if err := c.store.Ensure(); err != nil {
		return nil, err
	}
	lock := store.NewFileLock(c.store.LockPath())
	ok, err := lock.Acquire(c.ctx, 0)
	if err != nil {
		return nil, err
	}
	if !ok {
		holder := ""
		if pid := lock.HolderPID(); pid != 0 {
			holder = fmt.Sprintf(" (pid %d)", pid)
		}
		return nil, fail("store_locked", "another hkcli process%s is already running %s on this store", holder, what)
	}
	return lock, nil
}

var (
	unsafeName     = regexp.MustCompile(`[<>:"/\\|?*\x00-\x1f]`)
	windowsDevices = map[string]bool{"CON": true, "PRN": true, "AUX": true, "NUL": true}
)

func init() {
	for i := 1; i <= 9; i++ {
		windowsDevices["COM"+strconv.Itoa(i)] = true
		windowsDevices["LPT"+strconv.Itoa(i)] = true
	}
}

// safeFilename makes a remote-supplied name safe to use as a single file name
// on any OS.
func safeFilename(name string) string {
	base := ""
	for _, part := range strings.Split(strings.ReplaceAll(name, `\`, "/"), "/") {
		if part != "" && part != "." {
			base = part
		}
	}
	base = strings.Trim(unsafeName.ReplaceAllString(base, "_"), " .")
	if stem, _, _ := strings.Cut(base, "."); windowsDevices[strings.ToUpper(stem)] {
		base = "_" + base
	}
	if runes := []rune(base); len(runes) > 180 {
		base = string(runes[:180])
	}
	if base == "" {
		return "file"
	}
	return base
}

// hasSuffix reports whether a file name has an extension (pathlib's suffix).
func hasSuffix(name string) bool {
	i := strings.LastIndexByte(name, '.')
	return i > 0 && i < len(name)-1
}

const (
	yearMonthDay = `^([0-9]{4})-(1[0-2]|0[1-9]|[1-9])-(3[01]|[12][0-9]|0[1-9]|[1-9]| [1-9])`
	hourMinute   = `(2[0-3]|[0-1][0-9]|[0-9]):([0-5][0-9]|[0-9])$`
)

// The formats parseTime accepts; month, day, hour and minute may have one digit.
var timeFormats = []*regexp.Regexp{
	regexp.MustCompile(yearMonthDay + `\s+` + hourMinute),
	regexp.MustCompile(yearMonthDay + `[Tt]` + hourMinute),
	regexp.MustCompile(yearMonthDay + `$`),
}

// parseTime reads YYYY-MM-DD, "YYYY-MM-DD HH:MM", YYYY-MM-DDTHH:MM (local time)
// or epoch milliseconds; "" gives nil.
func parseTime(value string) (*int64, error) {
	if value == "" {
		return nil, nil
	}
	if strings.Trim(value, "0123456789") == "" {
		if ms, err := strconv.ParseInt(value, 10, 64); err == nil {
			return &ms, nil
		}
	}
	for _, format := range timeFormats {
		m := format.FindStringSubmatch(value)
		if m == nil {
			continue
		}
		n := make([]int, 6)
		for i, s := range m[1:] {
			n[i], _ = strconv.Atoi(strings.TrimSpace(s))
		}
		t := time.Date(n[0], time.Month(n[1]), n[2], n[3], n[4], 0, 0, time.Local)
		if t.Day() != n[2] {
			continue // day out of range for the month
		}
		ms := t.UnixMilli()
		return &ms, nil
	}
	return nil, fail("invalid_input", "cannot read time %s; use YYYY-MM-DD, 'YYYY-MM-DD HH:MM' or epoch milliseconds", pyjson.Repr(value))
}

// resolveChat accepts a chat or user id, a phone number, or a (unique) name and
// returns the id and display name. remote allows looking a +number up on Hike.
func (c *Ctx) resolveChat(spec string, remote bool) (string, string, error) {
	conn, err := c.Conn()
	if err != nil {
		return "", "", err
	}
	spec = pyjson.Strip(spec)
	if spec == "" {
		return "", "", fail("invalid_input", "empty chat / recipient")
	}
	if row, err := conn.GetChat(spec); err != nil || row != nil {
		return spec, pyjson.Str(pyjson.Or(row["name"], spec)), err
	}
	if contact, err := conn.GetContact(spec); err != nil || contact != nil {
		return spec, pyjson.Str(pyjson.Or(pyjson.Or(contact["name"], contact["phone"]), spec)), err
	}
	if phone.LooksLikePhone(spec) {
		return c.resolvePhone(conn, spec, remote)
	}
	needle := strings.ToLower(spec)
	rows, err := conn.NamedLike(needle, false)
	if err == nil && len(rows) == 0 {
		rows, err = conn.NamedLike(needle, true)
	}
	if err != nil {
		return "", "", err
	}
	candidates := pyjson.Object{}
	for _, r := range rows {
		candidates.Set(r.Str("id"), r["name"])
	}
	switch {
	case len(candidates) == 1:
		return candidates[0].Key, pyjson.Str(candidates[0].Value), nil
	case len(candidates) > 1:
		return "", "", fail("ambiguous", "'%s' matches %d chats/contacts: %s; use the id", spec, len(candidates), listCandidates(candidates[:min(10, len(candidates))]))
	}
	return "", "", fail("not_found", "no chat or contact matches '%s'; run 'hkcli sync' or use an id / phone number", spec)
}

func listCandidates(candidates pyjson.Object) string {
	parts := make([]string, len(candidates))
	for i, m := range candidates {
		parts[i] = fmt.Sprintf("%s (%s)", pyjson.Str(m.Value), m.Key)
	}
	return strings.Join(parts, ", ")
}

func (c *Ctx) resolvePhone(conn *db.Conn, spec string, remote bool) (string, string, error) {
	e164 := phone.NormalizeE164(spec)
	digits := phone.Digits(spec)
	rows, err := conn.PhoneOwners()
	if err != nil {
		return "", "", err
	}
	matches := pyjson.Object{}
	for _, r := range rows {
		if strings.HasSuffix(phone.Digits(r.Str("phone")), digits) {
			matches.Set(r.Str("id"), pyjson.Or(r["name"], r["phone"]))
		}
	}
	switch {
	case len(matches) == 1:
		return matches[0].Key, pyjson.Str(matches[0].Value), nil
	case len(matches) > 1:
		return "", "", fail("ambiguous", "%s matches several people: %s; use the id", spec, listCandidates(matches))
	}
	if remote && e164 != "" {
		client, err := c.API()
		if err != nil {
			return "", "", err
		}
		profiles, err := client.ProfilesByPhone(c.ctx, []string{e164})
		if err != nil {
			return "", "", err
		}
		for _, item := range profiles {
			profile, _ := item.(pyjson.Object)
			uid := protocol.AsStr(profile.Get("userId"))
			if uid == "" {
				continue
			}
			name := protocol.AsStr(profile.Get("name"))
			ph := protocol.AsStr(profile.Get("phone"))
			if ph == "" {
				ph = e164
			}
			if err := conn.UpsertContact(uid, db.Contact{Name: name, Phone: ph}, false); err != nil {
				return "", "", err
			}
			if err := conn.Commit(); err != nil {
				return "", "", err
			}
			if name == "" {
				name = e164
			}
			return uid, name, nil
		}
	}
	hint := ""
	if e164 == "" {
		hint = " (include the country code, e.g. +91...)"
	}
	return "", "", fail("not_found", "no Hike user found for %s%s", spec, hint)
}

func chatDict(r db.Row) pyjson.Object {
	return pyjson.O(
		"id", r["id"], "type", r["type"], "name", r["name"], "phone", r["phone"], "unread", r["unread"],
		"archived", r.Truthy("archived"), "muted", r.Truthy("muted"), "pinned", r.Truthy("pinned"),
		"blocked", r.Truthy("blocked"), "last_ts", r["last_ts"], "last_time", iso(r.Int("last_ts")),
		"last_text", r["last_text"],
	)
}

func messageDict(conn *db.Conn, r db.Row, me string, chatName any, withRaw bool) (pyjson.Object, error) {
	target := pyjson.Or(r["server_id"], r["client_id"])
	senderName := any("me")
	if !r.Truthy("from_me") {
		name, err := conn.DisplayName(r.Str("sender_id"), me)
		if err != nil {
			return nil, err
		}
		senderName = pyjson.Or(r["sender_name"], name)
	}
	reactions := pyjson.Object{}
	if pyjson.Truthy(target) {
		var err error
		if reactions, err = conn.ReactionsFor(r.Str("chat_id"), pyjson.Str(target)); err != nil {
			return nil, err
		}
	}
	data := pyjson.O(
		"id", target, "server_id", r["server_id"], "client_id", r["client_id"], "chat", r["chat_id"],
		"chat_name", chatName, "ts", r["ts"], "time", iso(r.Int("ts")), "from_me", r.Truthy("from_me"),
		"sender", r["sender_id"], "sender_name", senderName, "kind", r["kind"], "text", r["text"],
		"file_name", r["file_name"], "file_size", r["file_size"], "mime", r["mime"], "has_media", r.Truthy("file_id"),
		"state", r["state"], "reply_to", r["reply_to_id"], "edited", r.Truthy("edited"),
		"deleted", r.Truthy("deleted_for_all"), "starred", r.Truthy("starred"), "reactions", reactions,
	)
	switch r["kind"] {
	case "location":
		data = append(data, pyjson.O("latitude", r["latitude"], "longitude", r["longitude"], "address", r["address"])...)
	case "contact":
		data = append(data, pyjson.O("contact_name", r["contact_name"], "contact_phone", r["contact_phone"], "contact_user_id", r["contact_user_id"])...)
	}
	if withRaw {
		var raw any
		if text := r.Str("raw"); text != "" {
			raw, _ = pyjson.Loads([]byte(text))
		}
		data = append(data, pyjson.O("local_path", r["local_path"], "raw", raw)...)
	}
	return data, nil
}

func optStr(v any) string {
	if v == nil {
		return ""
	}
	return pyjson.Str(v)
}

func messageLine(m pyjson.Object, withChat bool) string {
	deleted := pyjson.Truthy(m.Get("deleted"))
	text := protocol.Summary(optStr(m.Get("kind")), optStr(m.Get("text")), optStr(m.Get("file_name")), deleted)
	if pyjson.Truthy(m.Get("edited")) && !deleted {
		text += " (edited)"
	}
	if reactions := m.Obj("reactions"); len(reactions) > 0 {
		emojis := make([]string, len(reactions))
		for i, r := range reactions {
			emojis[i] = pyjson.Str(r.Value)
		}
		text += "  [" + strings.Join(emojis, " ") + "]"
	}
	where := ""
	if chatName := m.Get("chat_name"); withChat && pyjson.Truthy(chatName) && chatName != m.Get("sender_name") {
		where = pyjson.Str(chatName) + " / "
	}
	ts, _ := protocol.AsInt(m.Get("ts"))
	return fmt.Sprintf("%s  %s%s: %s   (%s)", syncer.FmtTime(ts), where, pyjson.Str(m.Get("sender_name")), text, pyjson.Str(m.Get("id")))
}

// sendPacket publishes one packet: through a `sync` running on this store when there is one
// (Hike's relay handles a second connection of the same account badly), else over a
// short-lived relay connection of its own.
func (c *Ctx) sendPacket(packet pyjson.Object) (string, error) {
	payload := []byte(pyjson.DumpsCompact(packet))
	if serverID, handled, err := syncer.PublishViaSync(c.ctx, c.store.ControlPath(), payload, 1); handled {
		return serverID, err
	}
	client, err := c.API()
	if err != nil {
		return "", err
	}
	relay, err := syncer.OpenMQTT(c.ctx, client)
	if err != nil {
		return "", err
	}
	defer relay.Disconnect()
	return syncer.PublishPacket(c.ctx, relay, client.Session.UserID, packet, 1)
}

// storeSent records a message this device sent.
func (c *Ctx) storeSent(packet pyjson.Object, serverID string) error {
	conn, err := c.Conn()
	if err != nil {
		return err
	}
	me, err := c.Me()
	if err != nil {
		return err
	}
	msg := protocol.ParseMessage(packet, me, nil, "")
	if msg == nil {
		return nil
	}
	msg.ServerID = ""
	if protocol.IsServerID(serverID) {
		msg.ServerID = serverID
	}
	msg.State = "sent"
	if err := conn.EnsureChat(msg.ChatID, ""); err != nil {
		return err
	}
	if _, _, err := conn.UpsertMessage(msg); err != nil {
		return err
	}
	if err := conn.TouchChatLast(msg); err != nil {
		return err
	}
	return conn.Commit()
}

// replyInfo describes the stored message being replied to ("" = no reply).
func (c *Ctx) replyInfo(chatID, messageID string) (*protocol.Reply, error) {
	if messageID == "" {
		return nil, nil
	}
	conn, err := c.Conn()
	if err != nil {
		return nil, err
	}
	row, err := conn.FindMessage(chatID, messageID)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, fail("not_found", "message %s not found in this chat locally; run 'hkcli sync' first", messageID)
	}
	return &protocol.Reply{
		ID:       pyjson.Str(pyjson.Or(row["server_id"], row["client_id"])),
		SenderID: row.Str("sender_id"),
		Subtype:  row["subtype"],
		Text:     optStr(pyjson.Or(row["text"], "")),
	}, nil
}

// openFile shows a file with the system's default viewer, without waiting.
func openFile(path string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", path)
	case "darwin":
		cmd = exec.Command("open", path)
	default:
		cmd = exec.Command("xdg-open", path)
	}
	if cmd.Start() == nil {
		go cmd.Wait()
	}
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
