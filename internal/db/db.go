// Package db is the local SQLite mirror of chats, messages and contacts, with
// FTS5 search. The schema is the store's on-disk format: keep it compatible with
// existing stores, including those created by hikecli, hkcli's Python prototype.
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/vinaycodeaxe/hkcli/internal/protocol"
	"github.com/vinaycodeaxe/hkcli/internal/pyjson"

	_ "modernc.org/sqlite" // pure Go SQLite driver ("sqlite")
)

const schemaVersion = 1

const schema = `
CREATE TABLE IF NOT EXISTS meta (
    key TEXT PRIMARY KEY,
    value TEXT
);
CREATE TABLE IF NOT EXISTS chats (
    id TEXT PRIMARY KEY,
    type TEXT NOT NULL DEFAULT 'one_to_one',
    name TEXT,
    phone TEXT,
    unread INTEGER NOT NULL DEFAULT 0,
    api_unread INTEGER NOT NULL DEFAULT 0,
    undelivered INTEGER NOT NULL DEFAULT 0,
    archived INTEGER NOT NULL DEFAULT 0,
    muted INTEGER NOT NULL DEFAULT 0,
    pinned INTEGER NOT NULL DEFAULT 0,
    blocked INTEGER NOT NULL DEFAULT 0,
    last_ts INTEGER,
    last_text TEXT,
    last_sender TEXT,
    last_server_id TEXT,
    history_cursor TEXT,
    history_done INTEGER NOT NULL DEFAULT 0,
    updated_at INTEGER
);
CREATE TABLE IF NOT EXISTS messages (
    id INTEGER PRIMARY KEY,
    chat_id TEXT NOT NULL,
    server_id TEXT,
    client_id TEXT,
    sender_id TEXT NOT NULL,
    from_me INTEGER NOT NULL DEFAULT 0,
    ts INTEGER NOT NULL,
    kind TEXT NOT NULL,
    subtype TEXT,
    text TEXT,
    file_id TEXT,
    file_name TEXT,
    mime TEXT,
    file_size INTEGER,
    duration INTEGER,
    width INTEGER,
    height INTEGER,
    latitude REAL,
    longitude REAL,
    address TEXT,
    contact_name TEXT,
    contact_phone TEXT,
    contact_user_id TEXT,
    reply_to_id TEXT,
    reply_to_sender TEXT,
    reply_to_text TEXT,
    forwarded INTEGER NOT NULL DEFAULT 0,
    sender_name TEXT,
    state TEXT,
    edited INTEGER NOT NULL DEFAULT 0,
    deleted_for_me INTEGER NOT NULL DEFAULT 0,
    deleted_for_all INTEGER NOT NULL DEFAULT 0,
    starred INTEGER NOT NULL DEFAULT 0,
    pinned INTEGER NOT NULL DEFAULT 0,
    local_path TEXT,
    raw TEXT
);
CREATE UNIQUE INDEX IF NOT EXISTS messages_server_id ON messages(chat_id, server_id);
CREATE UNIQUE INDEX IF NOT EXISTS messages_client_id ON messages(chat_id, client_id);
CREATE INDEX IF NOT EXISTS messages_chat_ts ON messages(chat_id, ts);
CREATE TABLE IF NOT EXISTS reactions (
    chat_id TEXT NOT NULL,
    target_id TEXT NOT NULL,
    sender_id TEXT NOT NULL,
    emoji TEXT NOT NULL,
    ts INTEGER,
    PRIMARY KEY (chat_id, target_id, sender_id)
);
CREATE TABLE IF NOT EXISTS contacts (
    user_id TEXT PRIMARY KEY,
    name TEXT,
    phone TEXT,
    status TEXT,
    pic TEXT,
    in_address_book INTEGER NOT NULL DEFAULT 0,
    updated_at INTEGER
);
CREATE INDEX IF NOT EXISTS contacts_phone ON contacts(phone);
CREATE TABLE IF NOT EXISTS group_members (
    group_id TEXT NOT NULL,
    user_id TEXT NOT NULL,
    role TEXT,
    name TEXT,
    phone TEXT,
    PRIMARY KEY (group_id, user_id)
);
CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(
    text, file_name, content='messages', content_rowid='id', tokenize='unicode61 remove_diacritics 2'
);
CREATE TRIGGER IF NOT EXISTS messages_fts_insert AFTER INSERT ON messages BEGIN
    INSERT INTO messages_fts(rowid, text, file_name) VALUES (new.id, new.text, new.file_name);
END;
CREATE TRIGGER IF NOT EXISTS messages_fts_delete AFTER DELETE ON messages BEGIN
    INSERT INTO messages_fts(messages_fts, rowid, text, file_name) VALUES ('delete', old.id, old.text, old.file_name);
END;
CREATE TRIGGER IF NOT EXISTS messages_fts_update AFTER UPDATE OF text, file_name ON messages BEGIN
    INSERT INTO messages_fts(messages_fts, rowid, text, file_name) VALUES ('delete', old.id, old.text, old.file_name);
    INSERT INTO messages_fts(rowid, text, file_name) VALUES (new.id, new.text, new.file_name);
END;
`

var messageColumns = []string{
	"chat_id", "server_id", "client_id", "sender_id", "from_me", "ts", "kind", "subtype", "text",
	"file_id", "file_name", "mime", "file_size", "duration", "width", "height", "latitude", "longitude",
	"address", "contact_name", "contact_phone", "contact_user_id", "reply_to_id", "reply_to_sender",
	"reply_to_text", "forwarded", "sender_name", "state", "edited", "deleted_for_me", "deleted_for_all",
	"starred", "pinned", "raw",
}

var flagColumns = map[string]bool{"edited": true, "deleted_for_me": true, "deleted_for_all": true, "starred": true, "pinned": true}

var chatColumns = map[string]bool{
	"type": true, "name": true, "phone": true, "unread": true, "api_unread": true, "undelivered": true,
	"archived": true, "muted": true, "pinned": true, "blocked": true, "last_ts": true, "last_text": true,
	"last_sender": true, "last_server_id": true, "history_cursor": true, "history_done": true,
}

// Row is one result row keyed by column name.
type Row map[string]any

// Str returns a text column, or "" when it is NULL.
func (r Row) Str(col string) string {
	if s, ok := r[col].(string); ok {
		return s
	}
	if v := r[col]; v != nil {
		return pyjson.Str(v)
	}
	return ""
}

// Int returns an integer column, or 0 when it is NULL.
func (r Row) Int(col string) int64 {
	switch v := r[col].(type) {
	case int64:
		return v
	case float64:
		return int64(v)
	}
	return 0
}

// Truthy reports whether the value is set and not zero or empty.
func (r Row) Truthy(col string) bool { return pyjson.Truthy(r[col]) }

// Conn is one SQLite connection. As with Python's sqlite3 module, the first write
// opens a transaction that lasts until Commit or Rollback.
type Conn struct {
	db   *sql.DB
	conn *sql.Conn
	inTx bool
}

// Connect opens (creating if needed) the database at path and applies the schema.
func Connect(path string) (*Conn, error) {
	sqlDB, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(1)
	conn, err := sqlDB.Conn(context.Background())
	if err != nil {
		sqlDB.Close()
		return nil, err
	}
	c := &Conn{db: sqlDB, conn: conn}
	for _, stmt := range []string{
		"PRAGMA busy_timeout=30000", // before anything that may wait for another process
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		schema,
		fmt.Sprintf("PRAGMA user_version=%d", schemaVersion),
	} {
		if _, err := conn.ExecContext(context.Background(), stmt); err != nil {
			c.Close()
			return nil, err
		}
	}
	return c, nil
}

// Close closes the connection; an uncommitted transaction is rolled back.
func (c *Conn) Close() error {
	c.inTx = false
	err := c.conn.Close()
	if cerr := c.db.Close(); err == nil {
		err = cerr
	}
	return err
}

// exec runs a write, opening a transaction first if none is open.
func (c *Conn) exec(query string, args ...any) (sql.Result, error) {
	if !c.inTx {
		if _, err := c.conn.ExecContext(context.Background(), "BEGIN"); err != nil {
			return nil, err
		}
		c.inTx = true
	}
	return c.conn.ExecContext(context.Background(), query, args...)
}

// Commit commits the open transaction, if any.
func (c *Conn) Commit() error { return c.finish("COMMIT") }

// Rollback abandons the open transaction, if any.
func (c *Conn) Rollback() error { return c.finish("ROLLBACK") }

func (c *Conn) finish(stmt string) error {
	if !c.inTx {
		return nil
	}
	c.inTx = false
	_, err := c.conn.ExecContext(context.Background(), stmt)
	if err != nil && strings.Contains(err.Error(), "no transaction is active") {
		return nil // SQLite already rolled back after an error
	}
	return err
}

func (c *Conn) query(query string, args ...any) ([]Row, error) {
	rows, err := c.conn.QueryContext(context.Background(), query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var out []Row
	for rows.Next() {
		values := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := make(Row, len(cols))
		for i, col := range cols {
			if b, ok := values[i].([]byte); ok {
				values[i] = string(b)
			}
			row[col] = values[i]
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (c *Conn) queryRow(query string, args ...any) (Row, error) {
	rows, err := c.query(query, args...)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return rows[0], nil
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// FTS5Available reports whether SQLite was built with FTS5.
func FTS5Available() bool {
	probe, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return false
	}
	defer probe.Close()
	_, err = probe.Exec("CREATE VIRTUAL TABLE t USING fts5(x)")
	return err == nil
}

// SQLiteVersion is the version of the bundled SQLite library.
func SQLiteVersion() string {
	probe, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return "unknown"
	}
	defer probe.Close()
	var v string
	if probe.QueryRow("SELECT sqlite_version()").Scan(&v) != nil {
		return "unknown"
	}
	return v
}

// ---- meta ----------------------------------------------------------------

// GetMeta returns a meta value; ok is false when it is missing or NULL.
func (c *Conn) GetMeta(key string) (string, bool, error) {
	row, err := c.queryRow("SELECT value FROM meta WHERE key=?", key)
	if err != nil || row == nil || row["value"] == nil {
		return "", false, err
	}
	return row.Str("value"), true, nil
}

// SetMeta stores a meta value.
func (c *Conn) SetMeta(key, value string) error {
	_, err := c.exec("INSERT INTO meta(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, value)
	return err
}

// ---- chats ---------------------------------------------------------------

// EnsureChat creates the chat row if it does not exist ("" type = one_to_one).
func (c *Conn) EnsureChat(chatID, chatType string) error {
	if chatType == "" {
		chatType = "one_to_one"
	}
	_, err := c.exec("INSERT OR IGNORE INTO chats(id, type, updated_at) VALUES(?, ?, strftime('%s','now'))", chatID, chatType)
	return err
}

// UpdateChat sets chat columns given as alternating names and values,
// creating the chat first if needed.
func (c *Conn) UpdateChat(chatID string, fields ...any) error {
	chatType := ""
	var assignments []string
	var args []any
	for i := 0; i+1 < len(fields); i += 2 {
		col := fields[i].(string)
		if !chatColumns[col] {
			return fmt.Errorf("unknown chat field: %s", col)
		}
		if col == "type" {
			chatType, _ = fields[i+1].(string)
		}
		assignments = append(assignments, col+"=?")
		args = append(args, fields[i+1])
	}
	if err := c.EnsureChat(chatID, chatType); err != nil || len(assignments) == 0 {
		return err
	}
	args = append(args, chatID)
	_, err := c.exec("UPDATE chats SET "+strings.Join(assignments, ", ")+", updated_at=strftime('%s','now') WHERE id=?", args...)
	return err
}

// GetChat returns the chat row, or nil.
func (c *Conn) GetChat(chatID string) (Row, error) {
	return c.queryRow("SELECT * FROM chats WHERE id=?", chatID)
}

// TouchChatLast moves the chat's last-message preview forward if m is newer.
func (c *Conn) TouchChatLast(m *protocol.Message) error {
	if err := c.EnsureChat(m.ChatID, ""); err != nil {
		return err
	}
	preview := protocol.Summary(m.Kind, m.Text, m.FileName, m.IsDeletedForAll())
	_, err := c.exec(`UPDATE chats SET last_ts=?, last_text=?, last_sender=?, last_server_id=COALESCE(?, last_server_id)
           WHERE id=? AND (last_ts IS NULL OR last_ts <= ?)`,
		m.TS, preview, m.SenderID, nullable(m.ServerID), m.ChatID, m.TS)
	return err
}

// ChatFilter selects chats for ListChats.
type ChatFilter struct {
	UnreadOnly bool
	Query      string
	Type       string
	Limit      int
}

// ListChats returns pinned chats first, then the most recent.
func (c *Conn) ListChats(f ChatFilter) ([]Row, error) {
	var where []string
	var args []any
	if f.UnreadOnly {
		where = append(where, "unread > 0")
	}
	if f.Type != "" {
		where = append(where, "type = ?")
		args = append(args, f.Type)
	}
	if f.Query != "" {
		where = append(where, "(name LIKE ? OR phone LIKE ? OR id = ?)")
		args = append(args, "%"+f.Query+"%", "%"+f.Query+"%", f.Query)
	}
	query := "SELECT * FROM chats"
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY pinned DESC, COALESCE(last_ts, 0) DESC LIMIT ?"
	return c.query(query, append(args, f.Limit)...)
}

// ChatLastLocalTS is the newest stored message time in a chat; ok is false when
// the chat has no messages.
func (c *Conn) ChatLastLocalTS(chatID string) (int64, bool, error) {
	row, err := c.queryRow("SELECT MAX(ts) AS ts FROM messages WHERE chat_id=?", chatID)
	if err != nil || row == nil || row["ts"] == nil {
		return 0, false, err
	}
	return row.Int("ts"), true, nil
}

// IncrementUnread counts one more unread message in a chat.
func (c *Conn) IncrementUnread(chatID string) error {
	_, err := c.exec("UPDATE chats SET unread = unread + 1 WHERE id=?", chatID)
	return err
}

// MarkUnread flags a chat as unread without losing its count.
func (c *Conn) MarkUnread(chatID string) error {
	_, err := c.exec("UPDATE chats SET unread = MAX(unread, 1) WHERE id=?", chatID)
	return err
}

// UnnamedDirectChats lists one-to-one chats that have no real name yet.
func (c *Conn) UnnamedDirectChats() ([]string, error) {
	rows, err := c.query("SELECT id FROM chats WHERE type='one_to_one' AND (name IS NULL OR name = id)")
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.Str("id"))
	}
	return ids, err
}

// ---- messages ------------------------------------------------------------

// FindMessage finds a message by server or client id.
func (c *Conn) FindMessage(chatID, messageID string) (Row, error) {
	return c.queryRow("SELECT * FROM messages WHERE chat_id=? AND (server_id=? OR client_id=?) ORDER BY id LIMIT 1", chatID, messageID, messageID)
}

// LastReceived is the newest message from someone else that has a server id.
func (c *Conn) LastReceived(chatID string) (Row, error) {
	return c.queryRow("SELECT sender_id, server_id FROM messages WHERE chat_id=? AND from_me=0 AND server_id IS NOT NULL ORDER BY ts DESC, id DESC LIMIT 1", chatID)
}

func (c *Conn) existing(m *protocol.Message) (Row, error) {
	if m.ServerID != "" {
		row, err := c.queryRow("SELECT * FROM messages WHERE chat_id=? AND server_id=?", m.ChatID, m.ServerID)
		if err != nil || row != nil {
			return row, err
		}
	}
	if m.ClientID != "" {
		return c.queryRow("SELECT * FROM messages WHERE chat_id=? AND client_id=?", m.ChatID, m.ClientID)
	}
	return nil, nil
}

func messageValues(m *protocol.Message) map[string]any {
	optInt := func(p *int64) any {
		if p == nil {
			return nil
		}
		return *p
	}
	optFloat := func(p *float64) any {
		if p == nil {
			return nil
		}
		return *p
	}
	optBool := func(p *bool) any {
		if p == nil {
			return nil
		}
		return *p
	}
	return map[string]any{
		"chat_id": m.ChatID, "server_id": nullable(m.ServerID), "client_id": nullable(m.ClientID),
		"sender_id": m.SenderID, "from_me": boolInt(m.FromMe), "ts": m.TS, "kind": m.Kind,
		"subtype": nullable(m.Subtype), "text": nullable(m.Text), "file_id": nullable(m.FileID),
		"file_name": nullable(m.FileName), "mime": nullable(m.MIME), "file_size": optInt(m.FileSize),
		"duration": optInt(m.Duration), "width": optInt(m.Width), "height": optInt(m.Height),
		"latitude": optFloat(m.Latitude), "longitude": optFloat(m.Longitude), "address": nullable(m.Address),
		"contact_name": nullable(m.ContactName), "contact_phone": nullable(m.ContactPhone),
		"contact_user_id": nullable(m.ContactUserID), "reply_to_id": nullable(m.ReplyToID),
		"reply_to_sender": nullable(m.ReplyToSender), "reply_to_text": nullable(m.ReplyToText),
		"forwarded": boolInt(m.Forwarded), "sender_name": nullable(m.SenderName), "state": nullable(m.State),
		"edited": optBool(m.Edited), "deleted_for_me": optBool(m.DeletedForMe),
		"deleted_for_all": optBool(m.DeletedForAll), "starred": optBool(m.Starred), "pinned": optBool(m.Pinned),
		"raw": m.Raw,
	}
}

func intOf(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case float64:
		return int64(x)
	case bool:
		return boolInt(x)
	}
	return 0
}

func strOf(v any) string {
	s, _ := v.(string)
	return s
}

// UpsertMessage inserts m or merges it into the row with the same server or
// client id: known flags win, states only move up, ids are kept.
func (c *Conn) UpsertMessage(m *protocol.Message) (rowID int64, inserted bool, err error) {
	values := messageValues(m)
	row, err := c.existing(m)
	if err != nil {
		return 0, false, err
	}
	args := make([]any, len(messageColumns))
	if row == nil {
		for i, col := range messageColumns {
			if flagColumns[col] {
				values[col] = boolInt(values[col] == true)
			}
			args[i] = values[col]
		}
		marks := strings.TrimSuffix(strings.Repeat("?, ", len(messageColumns)), ", ")
		res, err := c.exec("INSERT INTO messages("+strings.Join(messageColumns, ", ")+") VALUES("+marks+")", args...)
		if err != nil {
			return 0, false, err
		}
		rowID, err = res.LastInsertId()
		if err != nil {
			return 0, false, err
		}
		inserted = true
	} else {
		merged := make(map[string]any, len(messageColumns))
		for _, col := range messageColumns {
			nv, ov := values[col], row[col]
			switch {
			case flagColumns[col]:
				merged[col] = ov
				if nv != nil {
					merged[col] = boolInt(nv == true)
				}
			case col == "state":
				merged[col] = ov
				if protocol.Rank(strOf(nv)) > protocol.Rank(strOf(ov)) {
					merged[col] = nv
				}
			case col == "server_id" || col == "client_id":
				merged[col] = pyjson.Or(ov, nv)
			case col == "from_me" || col == "forwarded":
				merged[col] = max(intOf(ov), intOf(nv))
			default:
				merged[col] = ov
				if nv != nil {
					merged[col] = nv
				}
			}
		}
		if pyjson.Truthy(merged["deleted_for_all"]) {
			merged["text"] = nil
		}
		for i, col := range messageColumns {
			args[i] = merged[col]
		}
		rowID = row.Int("id")
		_, err := c.exec("UPDATE messages SET "+strings.Join(messageColumns, "=?, ")+"=? WHERE id=?", append(args, rowID)...)
		if err != nil {
			return 0, false, err
		}
	}
	if len(m.Reactions) > 0 {
		target := m.ID()
		if _, err := c.exec("DELETE FROM reactions WHERE chat_id=? AND target_id=?", m.ChatID, target); err != nil {
			return 0, false, err
		}
		for _, r := range m.Reactions {
			if _, err := c.exec("INSERT INTO reactions(chat_id, target_id, sender_id, emoji, ts) VALUES(?, ?, ?, ?, ?)", m.ChatID, target, r.Sender, r.Emoji, m.TS); err != nil {
				return 0, false, err
			}
		}
	}
	return rowID, inserted, nil
}

// ListMessages returns the latest limit messages (older than before, if set),
// oldest first.
func (c *Conn) ListMessages(chatID string, limit int, before *int64) ([]Row, error) {
	query := "SELECT * FROM messages WHERE chat_id=? AND deleted_for_me=0"
	args := []any{chatID}
	if before != nil {
		query += " AND ts < ?"
		args = append(args, *before)
	}
	rows, err := c.query(query+" ORDER BY ts DESC, id DESC LIMIT ?", append(args, limit)...)
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
	return rows, err
}

// ErrEmptyQuery means a search had no words.
var ErrEmptyQuery = errors.New("search query is empty")

// ToFTSQuery quotes every word for FTS5; a trailing * keeps prefix matching.
func ToFTSQuery(query string) (string, error) {
	var terms []string
	for _, token := range strings.FieldsFunc(query, pyjson.IsSpace) {
		prefix := strings.HasSuffix(token, "*") && len(token) > 1
		word := token
		if prefix {
			word = token[:len(token)-1]
		}
		word = strings.ReplaceAll(word, `"`, `""`)
		if word == "" {
			continue
		}
		if prefix {
			terms = append(terms, `"`+word+`"*`)
		} else {
			terms = append(terms, `"`+word+`"`)
		}
	}
	if len(terms) == 0 {
		return "", ErrEmptyQuery
	}
	return strings.Join(terms, " "), nil
}

// SearchMessages runs a full-text search, newest first.
func (c *Conn) SearchMessages(query, chatID string, limit int) ([]Row, error) {
	fts, err := ToFTSQuery(query)
	if err != nil {
		return nil, err
	}
	sqlText := `SELECT m.* FROM messages_fts JOIN messages m ON m.id = messages_fts.rowid
             WHERE messages_fts MATCH ? AND m.deleted_for_me=0 AND m.deleted_for_all=0`
	args := []any{fts}
	if chatID != "" {
		sqlText += " AND m.chat_id=?"
		args = append(args, chatID)
	}
	return c.query(sqlText+" ORDER BY m.ts DESC LIMIT ?", append(args, limit)...)
}

// ReactionsFor maps each reacting user to their emoji.
func (c *Conn) ReactionsFor(chatID, targetID string) (pyjson.Object, error) {
	rows, err := c.query("SELECT sender_id, emoji FROM reactions WHERE chat_id=? AND target_id=?", chatID, targetID)
	out := pyjson.Object{}
	for _, r := range rows {
		out.Set(r.Str("sender_id"), r["emoji"])
	}
	return out, err
}

// SetReaction stores a reaction, or removes it when emoji is "".
func (c *Conn) SetReaction(chatID, targetID, senderID, emoji string, ts int64) error {
	if emoji != "" {
		_, err := c.exec(`INSERT INTO reactions(chat_id, target_id, sender_id, emoji, ts) VALUES(?, ?, ?, ?, ?)
               ON CONFLICT(chat_id, target_id, sender_id) DO UPDATE SET emoji=excluded.emoji, ts=excluded.ts`,
			chatID, targetID, senderID, emoji, ts)
		return err
	}
	_, err := c.exec("DELETE FROM reactions WHERE chat_id=? AND target_id=? AND sender_id=?", chatID, targetID, senderID)
	return err
}

// MarkMyMessages raises the state of my messages with seq <= upTo (Hike
// receipts are cumulative) and returns how many changed.
func (c *Conn) MarkMyMessages(chatID string, upTo int64, state string) (int, error) {
	rank := protocol.StateRank[state]
	rows, err := c.query("SELECT id, server_id, state FROM messages WHERE chat_id=? AND from_me=1 AND server_id IS NOT NULL", chatID)
	if err != nil {
		return 0, err
	}
	updated := 0
	for _, r := range rows {
		seq, _ := protocol.ServerSeq(r.Str("server_id"))
		if seq > upTo || protocol.Rank(r.Str("state")) >= rank {
			continue
		}
		if _, err := c.exec("UPDATE messages SET state=? WHERE id=?", state, r["id"]); err != nil {
			return updated, err
		}
		updated++
	}
	return updated, nil
}

// ApplyMessageAction applies an edit, delete, star or pin ("ma" packet) and
// returns the number of messages changed.
func (c *Conn) ApplyMessageAction(chatID string, action any, targetID string, text, value any, byMe bool) (int64, error) {
	const where = "chat_id=? AND (server_id=? OR client_id=?)"
	args := []any{chatID, targetID, targetID}
	var query string
	switch action {
	case protocol.MAEdit:
		query = "UPDATE messages SET text=?, edited=1 WHERE " + where + " AND deleted_for_all=0"
		args = append([]any{text}, args...)
	case protocol.MADeleteForAll:
		query = "UPDATE messages SET text=NULL, deleted_for_all=1 WHERE " + where
	case protocol.MADeleteForMe:
		if !byMe {
			return 0, nil
		}
		query = "UPDATE messages SET deleted_for_me=1 WHERE " + where
	case protocol.MAStar:
		query = "UPDATE messages SET starred=? WHERE " + where
		args = append([]any{boolInt(pyjson.Truthy(value))}, args...)
	case protocol.MAPin:
		query = "UPDATE messages SET pinned=? WHERE " + where
		args = append([]any{boolInt(pyjson.Truthy(value))}, args...)
	default:
		return 0, nil
	}
	res, err := c.exec(query, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DeleteForMeUpTo hides every message in a chat up to cutoff (a cleared chat).
func (c *Conn) DeleteForMeUpTo(chatID string, cutoff int64) error {
	_, err := c.exec("UPDATE messages SET deleted_for_me=1 WHERE chat_id=? AND ts <= ?", chatID, cutoff)
	return err
}

// SetLocalPath records where a message's media was saved.
func (c *Conn) SetLocalPath(rowID any, path string) error {
	_, err := c.exec("UPDATE messages SET local_path=? WHERE id=?", path, rowID)
	return err
}

// ---- contacts and groups -------------------------------------------------

// Contact holds contact fields to store; "" and nil leave a field unchanged.
type Contact struct {
	Name, Phone, Status, Pic string
	InAddressBook            *bool
}

// UpsertContact inserts or updates a contact. Without overwriteName an
// existing name is kept.
func (c *Conn) UpsertContact(userID string, ct Contact, overwriteName bool) error {
	nameSQL := "COALESCE(contacts.name, excluded.name)"
	if overwriteName {
		nameSQL = "COALESCE(excluded.name, contacts.name)"
	}
	var inBook any
	if ct.InAddressBook != nil {
		inBook = *ct.InAddressBook
	}
	_, err := c.exec(`INSERT INTO contacts(user_id, name, phone, status, pic, in_address_book, updated_at)
            VALUES(?, ?, ?, ?, ?, ?, strftime('%s','now'))
            ON CONFLICT(user_id) DO UPDATE SET
              name=`+nameSQL+`,
              phone=COALESCE(excluded.phone, contacts.phone),
              status=COALESCE(excluded.status, contacts.status),
              pic=COALESCE(excluded.pic, contacts.pic),
              in_address_book=CASE WHEN ? IS NULL THEN contacts.in_address_book ELSE excluded.in_address_book END,
              updated_at=excluded.updated_at`,
		userID, nullable(ct.Name), nullable(ct.Phone), nullable(ct.Status), nullable(ct.Pic), boolInt(inBook == true), inBook)
	return err
}

// GetContact returns the contact row, or nil.
func (c *Conn) GetContact(userID string) (Row, error) {
	return c.queryRow("SELECT * FROM contacts WHERE user_id=?", userID)
}

// ListContacts returns address-book contacts first, then by name.
func (c *Conn) ListContacts(query string, limit int) ([]Row, error) {
	sqlText := "SELECT * FROM contacts"
	var args []any
	if query != "" {
		sqlText += " WHERE name LIKE ? OR phone LIKE ? OR user_id = ?"
		args = append(args, "%"+query+"%", "%"+query+"%", query)
	}
	sqlText += " ORDER BY in_address_book DESC, COALESCE(name, phone, user_id) COLLATE NOCASE LIMIT ?"
	return c.query(sqlText, append(args, limit)...)
}

// PhoneOwners lists every contact and chat with a phone number (id, name, phone).
func (c *Conn) PhoneOwners() ([]Row, error) {
	return c.query("SELECT user_id AS id, name, phone FROM contacts WHERE phone IS NOT NULL UNION SELECT id, name, phone FROM chats WHERE phone IS NOT NULL")
}

// NamedLike lists chats and contacts (id, name) whose lowercased name equals
// needle, or contains it when partial is set.
func (c *Conn) NamedLike(needle string, partial bool) ([]Row, error) {
	if partial {
		like := "%" + needle + "%"
		return c.query("SELECT id, name FROM chats WHERE lower(name) LIKE ? UNION SELECT user_id, name FROM contacts WHERE lower(name) LIKE ?", like, like)
	}
	return c.query("SELECT id, name FROM chats WHERE lower(name)=? UNION SELECT user_id, name FROM contacts WHERE lower(name)=?", needle, needle)
}

// DisplayName is "me", the contact's name or phone, the chat name, or the id.
func (c *Conn) DisplayName(userID, me string) (string, error) {
	if userID == "" {
		return "", nil
	}
	if me != "" && userID == me {
		return "me", nil
	}
	row, err := c.queryRow("SELECT name, phone FROM contacts WHERE user_id=?", userID)
	if err != nil {
		return "", err
	}
	if row != nil && (row.Truthy("name") || row.Truthy("phone")) {
		return pyjson.Str(pyjson.Or(row["name"], row["phone"])), nil
	}
	chat, err := c.queryRow("SELECT name FROM chats WHERE id=? AND type='one_to_one'", userID)
	if err != nil {
		return "", err
	}
	if chat != nil && chat.Truthy("name") {
		return chat.Str("name"), nil
	}
	return userID, nil
}

// UnnamedSenders lists people who sent messages but have no stored name.
func (c *Conn) UnnamedSenders(me string, limit int) ([]string, error) {
	rows, err := c.query(`SELECT DISTINCT m.sender_id FROM messages m
           LEFT JOIN contacts c ON c.user_id = m.sender_id
           WHERE m.from_me=0 AND m.sender_id != '' AND m.sender_id != ? AND (c.user_id IS NULL OR c.name IS NULL)
           LIMIT ?`, me, limit)
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.Str("sender_id"))
	}
	return ids, err
}

// ReplaceGroupMembers stores a group's member list from Hike's group API.
func (c *Conn) ReplaceGroupMembers(groupID string, members []any) error {
	if _, err := c.exec("DELETE FROM group_members WHERE group_id=?", groupID); err != nil {
		return err
	}
	for _, item := range members {
		m, _ := item.(pyjson.Object)
		if !pyjson.Truthy(m.Get("userId")) {
			continue
		}
		if _, err := c.exec("INSERT OR REPLACE INTO group_members(group_id, user_id, role, name, phone) VALUES(?, ?, ?, ?, ?)",
			groupID, pyjson.Str(m.Get("userId")), m.Get("role"), m.Get("name"), m.Get("phoneNumber")); err != nil {
			return err
		}
	}
	return nil
}

// Stats counts what the store holds.
type Stats struct {
	Chats, Messages, Contacts int64
}

// Stats counts chats, messages and contacts.
func (c *Conn) Stats() (Stats, error) {
	var s Stats
	for _, item := range []struct {
		dst   *int64
		table string
	}{{&s.Chats, "chats"}, {&s.Messages, "messages"}, {&s.Contacts, "contacts"}} {
		row, err := c.queryRow("SELECT COUNT(*) AS n FROM " + item.table)
		if err != nil {
			return s, err
		}
		*item.dst = row.Int("n")
	}
	return s, nil
}
