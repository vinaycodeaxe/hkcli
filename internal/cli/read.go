package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/vinaycodeaxe/hkcli/internal/db"
	"github.com/vinaycodeaxe/hkcli/internal/protocol"
	"github.com/vinaycodeaxe/hkcli/internal/pyjson"
	"github.com/vinaycodeaxe/hkcli/internal/syncer"
)

func (a *app) chatsCommand() *cobra.Command {
	var unread, groups, direct bool
	var query string
	var limit int
	list := leaf("list", "list chats, most recent first", a.run(func(c *Ctx, _ *cobra.Command, _ []string) error {
		chatType := ""
		if groups {
			chatType = "group"
		} else if direct {
			chatType = "one_to_one"
		}
		return c.runChatsList(db.ChatFilter{UnreadOnly: unread, Query: query, Type: chatType, Limit: limit})
	}))
	f := list.Flags()
	f.BoolVar(&unread, "unread", false, "only chats with unread messages")
	f.BoolVar(&groups, "groups", false, "only groups")
	f.BoolVar(&direct, "direct", false, "only one-to-one chats")
	f.StringVar(&query, "query", "", "filter by name, phone or id")
	f.IntVar(&limit, "limit", 50, "")

	var chat string
	markRead := leaf("mark-read", "mark a chat as read (sends a read receipt)", a.run(func(c *Ctx, _ *cobra.Command, _ []string) error {
		return c.runMarkRead(chat)
	}))
	markRead.Flags().StringVar(&chat, "chat", "", "chat name, phone number or id")
	required(markRead, "chat")
	return a.group("chats", "list chats, mark them read", list, markRead)
}

func (c *Ctx) runChatsList(filter db.ChatFilter) error {
	conn, err := c.Conn()
	if err != nil {
		return err
	}
	rows, err := conn.ListChats(filter)
	if err != nil {
		return err
	}
	data := make([]pyjson.Object, len(rows))
	for i, r := range rows {
		data[i] = chatDict(r)
	}
	c.output(data, func() {
		if len(rows) == 0 {
			c.println("No chats stored yet. Run: hkcli sync --once")
			return
		}
		table := make([][]any, len(rows))
		for i, r := range rows {
			kind := "direct"
			if r["type"] == "group" {
				kind = "group"
			}
			table[i] = []any{pyjson.Or(r["name"], r["id"]), kind, pyjson.Or(r["unread"], ""), syncer.FmtTime(r.Int("last_ts")), r["last_text"], r["id"]}
		}
		c.printTable([]string{"NAME", "TYPE", "UNREAD", "LAST", "PREVIEW", "ID"}, table, map[int]int{0: 28, 4: 50})
	})
	return nil
}

func (c *Ctx) runMarkRead(spec string) error {
	if err := c.requireWritable("chats mark-read"); err != nil {
		return err
	}
	conn, err := c.Conn()
	if err != nil {
		return err
	}
	session, err := c.Session()
	if err != nil {
		return err
	}
	me := session.UserID
	chatID, name, err := c.resolveChat(spec, false)
	if err != nil {
		return err
	}
	chat, err := conn.GetChat(chatID)
	if err != nil {
		return err
	}
	last, err := conn.LastReceived(chatID)
	if err != nil {
		return err
	}
	var sender, serverID string
	if last != nil {
		sender, serverID = last.Str("sender_id"), last.Str("server_id")
	}
	if serverID == "" && chat != nil && chat.Truthy("last_server_id") && chat["last_sender"] != nil && chat["last_sender"] != me {
		sender, serverID = chat.Str("last_sender"), chat.Str("last_server_id")
	}
	if serverID == "" {
		return fail("not_found", "no received message to mark as read in this chat (run 'hkcli sync --once' first)")
	}
	var packet pyjson.Object
	if chat != nil && chat.Truthy("api_unread") {
		packet = protocol.BuildFirstMarkRead(me, chatID, serverID) // what Hike Web sends for chats the API reported unread
	} else {
		packet = protocol.BuildMarkRead(me, chatID, sender, serverID)
	}
	if _, err := c.sendPacket(packet); err != nil {
		return err
	}
	if err := conn.UpdateChat(chatID, "unread", 0, "api_unread", 0); err != nil {
		return err
	}
	if err := conn.Commit(); err != nil {
		return err
	}
	c.output(pyjson.O("chat", chatID, "name", name, "up_to", serverID), func() { c.println(fmt.Sprintf("Marked %s as read.", name)) })
	return nil
}

func (a *app) messagesCommand() *cobra.Command {
	var listChat, before string
	var listLimit int
	list := leaf("list", "show the latest messages in a chat (oldest first)", a.run(func(c *Ctx, _ *cobra.Command, _ []string) error {
		return c.runMessagesList(listChat, listLimit, before)
	}))
	f := list.Flags()
	f.StringVar(&listChat, "chat", "", "chat name, phone number or id")
	f.IntVar(&listLimit, "limit", 50, "")
	f.StringVar(&before, "before", "", "only messages before this time (YYYY-MM-DD[ HH:MM] or epoch ms)")
	required(list, "chat")

	var searchChat string
	var searchLimit int
	search := leaf("search <query>", "full-text search across stored messages", a.run(func(c *Ctx, _ *cobra.Command, args []string) error {
		return c.runMessagesSearch(args[0], searchChat, searchLimit)
	}))
	search.Args = func(cmd *cobra.Command, args []string) error {
		switch {
		case len(args) == 0:
			return &usageError{"the following arguments are required: query"}
		case len(args) > 1:
			return &usageError{"unrecognized arguments: " + strings.Join(args[1:], " ")}
		}
		return nil
	}
	search.Flags().StringVar(&searchChat, "chat", "", "limit to one chat")
	search.Flags().IntVar(&searchLimit, "limit", 50, "")

	var showChat, showID string
	show := leaf("show", "show one message with all details", a.run(func(c *Ctx, _ *cobra.Command, _ []string) error {
		return c.runMessagesShow(showChat, showID)
	}))
	show.Flags().StringVar(&showChat, "chat", "", "")
	show.Flags().StringVar(&showID, "id", "", "message id (server or client id)")
	required(show, "chat", "id")
	return a.group("messages", "read and search stored messages", list, search, show)
}

func (c *Ctx) runMessagesList(spec string, limit int, before string) error {
	conn, err := c.Conn()
	if err != nil {
		return err
	}
	chatID, name, err := c.resolveChat(spec, false)
	if err != nil {
		return err
	}
	me, err := c.Me()
	if err != nil {
		return err
	}
	cutoff, err := parseTime(before)
	if err != nil {
		return err
	}
	rows, err := conn.ListMessages(chatID, limit, cutoff)
	if err != nil {
		return err
	}
	data := make([]pyjson.Object, len(rows))
	for i, r := range rows {
		if data[i], err = messageDict(conn, r, me, name, false); err != nil {
			return err
		}
	}
	c.output(data, func() {
		if len(data) == 0 {
			c.println(fmt.Sprintf("No messages stored for %s. Run 'hkcli sync --once' or 'hkcli history backfill --chat ...'.", name))
		}
		for _, m := range data {
			c.println(messageLine(m, false))
		}
	})
	return nil
}

func (c *Ctx) runMessagesSearch(query, spec string, limit int) error {
	conn, err := c.Conn()
	if err != nil {
		return err
	}
	chatID := ""
	if spec != "" {
		if chatID, _, err = c.resolveChat(spec, false); err != nil {
			return err
		}
	}
	me, err := c.Me()
	if err != nil {
		return err
	}
	rows, err := conn.SearchMessages(query, chatID, limit)
	if errors.Is(err, db.ErrEmptyQuery) {
		return fail("invalid_input", "%s", err.Error())
	}
	if err != nil {
		return fail("invalid_input", "search failed: %v", err)
	}
	names := map[string]any{}
	data := make([]pyjson.Object, len(rows))
	for i, r := range rows {
		id := r.Str("chat_id")
		if _, ok := names[id]; !ok {
			chat, err := conn.GetChat(id)
			if err != nil {
				return err
			}
			names[id] = pyjson.Or(chat["name"], r["chat_id"])
		}
		if data[i], err = messageDict(conn, r, me, names[id], false); err != nil {
			return err
		}
	}
	c.output(data, func() {
		if len(data) == 0 {
			c.println("No matches.")
		}
		for _, m := range data {
			c.println(messageLine(m, true))
		}
	})
	return nil
}

func (c *Ctx) runMessagesShow(spec, id string) error {
	conn, err := c.Conn()
	if err != nil {
		return err
	}
	chatID, name, err := c.resolveChat(spec, false)
	if err != nil {
		return err
	}
	row, err := conn.FindMessage(chatID, id)
	if err != nil {
		return err
	}
	if row == nil {
		return fail("not_found", "message %s not found in %s", id, name)
	}
	me, err := c.Me()
	if err != nil {
		return err
	}
	data, err := messageDict(conn, row, me, name, true)
	if err != nil {
		return err
	}
	c.output(data, func() {
		for _, key := range []string{"id", "server_id", "client_id", "chat_name", "time", "sender_name", "kind", "state", "text", "file_name", "file_size", "mime", "reply_to", "reactions", "local_path"} {
			v := data.Get(key)
			if obj, ok := v.(pyjson.Object); v == nil || v == "" || (ok && len(obj) == 0) {
				continue
			}
			c.println(fmt.Sprintf("%11s: %s", key, pyjson.Str(v)))
		}
	})
	return nil
}
