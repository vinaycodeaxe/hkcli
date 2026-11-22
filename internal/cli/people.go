package cli

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/vinaycodeaxe/hkcli/internal/db"
	"github.com/vinaycodeaxe/hkcli/internal/media"
	"github.com/vinaycodeaxe/hkcli/internal/pyjson"
	"github.com/vinaycodeaxe/hkcli/internal/store"
	"github.com/vinaycodeaxe/hkcli/internal/syncer"
)

func (a *app) contactsCommand() *cobra.Command {
	var query string
	var limit int
	list := leaf("list", "list stored contacts", a.run(func(c *Ctx, _ *cobra.Command, _ []string) error {
		return c.runContactsList(query, limit)
	}))
	list.Flags().StringVar(&query, "query", "", "filter by name, phone or id")
	list.Flags().IntVar(&limit, "limit", 200, "")
	refresh := leaf("refresh", "download contacts and profile names from Hike", a.run(func(c *Ctx, _ *cobra.Command, _ []string) error {
		return c.runContactsRefresh()
	}))
	return a.group("contacts", "list or refresh contacts", list, refresh)
}

func (c *Ctx) runContactsList(query string, limit int) error {
	conn, err := c.Conn()
	if err != nil {
		return err
	}
	rows, err := conn.ListContacts(query, limit)
	if err != nil {
		return err
	}
	data := make([]pyjson.Object, len(rows))
	table := make([][]any, len(rows))
	for i, r := range rows {
		data[i] = pyjson.O("user_id", r["user_id"], "name", r["name"], "phone", r["phone"], "status", r["status"], "in_address_book", r.Truthy("in_address_book"))
		saved := ""
		if r.Truthy("in_address_book") {
			saved = "yes"
		}
		table[i] = []any{pyjson.Or(r["name"], ""), pyjson.Or(r["phone"], ""), saved, r["user_id"]}
	}
	c.output(data, func() {
		if len(rows) == 0 {
			c.println("No contacts stored yet. Run: hkcli contacts refresh")
			return
		}
		c.printTable([]string{"NAME", "PHONE", "SAVED", "USER ID"}, table, map[int]int{0: 30})
	})
	return nil
}

func (c *Ctx) runContactsRefresh() error {
	if err := c.requireWritable("contacts refresh"); err != nil {
		return err
	}
	client, err := c.API()
	if err != nil {
		return err
	}
	conn, err := c.Conn()
	if err != nil {
		return err
	}
	s := syncer.New(conn, client, client.Session.UserID, syncer.NewEmitter(false, true), syncer.DefaultOptions())
	count, err := s.SyncContacts(c.ctx)
	if err != nil {
		return err
	}
	named, err := s.ResolveNames(c.ctx)
	if err != nil {
		return err
	}
	c.output(pyjson.O("contacts", count, "profiles", named), func() {
		c.println(fmt.Sprintf("Updated %d contacts and %d profile names.", count, named))
	})
	return nil
}

func (a *app) groupsCommand() *cobra.Command {
	var limit int
	list := leaf("list", "list groups", a.run(func(c *Ctx, _ *cobra.Command, _ []string) error {
		return c.runGroupsList(limit)
	}))
	list.Flags().IntVar(&limit, "limit", 100, "")
	var group string
	info := leaf("info", "show a group's details and members (live from Hike)", a.run(func(c *Ctx, _ *cobra.Command, _ []string) error {
		return c.runGroupsInfo(group)
	}))
	info.Flags().StringVar(&group, "group", "", "group name or id")
	required(info, "group")
	return a.group("groups", "list groups, show members", list, info)
}

func (c *Ctx) runGroupsList(limit int) error {
	conn, err := c.Conn()
	if err != nil {
		return err
	}
	rows, err := conn.ListChats(db.ChatFilter{Type: "group", Limit: limit})
	if err != nil {
		return err
	}
	data := make([]pyjson.Object, len(rows))
	table := make([][]any, len(rows))
	for i, r := range rows {
		data[i] = chatDict(r)
		table[i] = []any{pyjson.Or(r["name"], r["id"]), pyjson.Or(r["unread"], ""), syncer.FmtTime(r.Int("last_ts")), r["id"]}
	}
	c.output(data, func() {
		if len(rows) == 0 {
			c.println("No groups stored yet. Run: hkcli sync --once")
			return
		}
		c.printTable([]string{"NAME", "UNREAD", "LAST", "ID"}, table, map[int]int{0: 40})
	})
	return nil
}

func (c *Ctx) runGroupsInfo(spec string) error {
	conn, err := c.Conn()
	if err != nil {
		return err
	}
	groupID, name, err := c.resolveChat(spec, false)
	if err != nil {
		return err
	}
	client, err := c.API()
	if err != nil {
		return err
	}
	info, err := client.Group(c.ctx, groupID)
	if err != nil {
		return err
	}
	members, _ := info.Get("members").([]any)
	if !c.readOnly {
		if pyjson.Truthy(info.Get("name")) {
			if err := conn.UpdateChat(groupID, "type", "group", "name", info.Get("name")); err != nil {
				return err
			}
		}
		if err := conn.ReplaceGroupMembers(groupID, members); err != nil {
			return err
		}
		if err := conn.Commit(); err != nil {
			return err
		}
	}
	list := make([]pyjson.Object, 0, len(members))
	table := make([][]any, 0, len(members))
	for _, item := range members {
		m, _ := item.(pyjson.Object)
		list = append(list, pyjson.O("user_id", m.Get("userId"), "name", m.Get("name"), "phone", m.Get("phoneNumber"), "role", m.Get("role")))
		table = append(table, []any{pyjson.Or(m.Get("name"), ""), pyjson.Or(m.Get("phoneNumber"), ""), strings.ToLower(optStr(pyjson.Or(m.Get("role"), ""))), m.Get("userId")})
	}
	data := pyjson.O(
		"id", groupID, "name", pyjson.Or(info.Get("name"), name), "description", info.Get("description"),
		"created_by", info.Get("createdBy"), "created_at", info.Get("createdAt"), "my_permissions", info.Get("myPermissions"),
		"members", list,
	)
	c.output(data, func() {
		c.println(fmt.Sprintf("%s  (%s)", pyjson.Str(data.Get("name")), groupID))
		if description := data.Get("description"); pyjson.Truthy(description) {
			c.println(pyjson.Str(description))
		}
		c.printTable([]string{"MEMBER", "PHONE", "ROLE", "USER ID"}, table, nil)
	})
	return nil
}

func (a *app) mediaCommand() *cobra.Command {
	var chat, id, out string
	download := leaf("download", "download a message's photo, video, audio or file", a.run(func(c *Ctx, _ *cobra.Command, _ []string) error {
		return c.runMediaDownload(chat, id, out)
	}))
	f := download.Flags()
	f.StringVar(&chat, "chat", "", "")
	f.StringVar(&id, "id", "", "message id")
	f.StringVar(&out, "out", "", "file or folder to save to (default: <store>/media/<chat>/)")
	required(download, "chat", "id")
	return a.group("media", "download attachments", download)
}

func (c *Ctx) runMediaDownload(spec, id, out string) error {
	if err := c.requireWritable("media download"); err != nil {
		return err
	}
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
	if !row.Truthy("file_id") {
		return fail("no_media", "that message has no downloadable media")
	}
	fileName := safeFilename(pyjson.Str(pyjson.Or(row["file_name"], row["file_id"])))
	if !hasSuffix(fileName) && row.Truthy("mime") {
		fileName += media.GuessExtension(row.Str("mime"))
	}
	dest := filepath.Join(c.store.MediaDir(), safeFilename(chatID), fileName)
	if out != "" {
		dest = filepath.Clean(store.ExpandUser(out))
		if isDir(dest) {
			dest = filepath.Join(dest, fileName)
		}
	}
	client, err := c.API()
	if err != nil {
		return err
	}
	saved, err := media.Download(c.ctx, client, row.Str("file_id"), dest)
	if err != nil {
		return err
	}
	if err := conn.SetLocalPath(row["id"], saved.Path); err != nil {
		return err
	}
	if err := conn.Commit(); err != nil {
		return err
	}
	c.output(pyjson.O("path", saved.Path, "size", saved.Size, "mime", saved.MIME), func() {
		c.println(fmt.Sprintf("Saved %d bytes to %s", saved.Size, saved.Path))
	})
	return nil
}
