package cli

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/vinaycodeaxe/hkcli/internal/pyjson"
	"github.com/vinaycodeaxe/hkcli/internal/syncer"
)

type syncOptions struct {
	once         bool
	presence     choice
	noReceipts   bool
	idleExit     float64
	maxReconnect float64
	maxChats     int
	batch        int
	pages        int
}

func (a *app) syncCommand() *cobra.Command {
	o := &syncOptions{presence: choice{value: "quiet", allowed: []string{"quiet", "normal"}}}
	cmd := leaf("sync", "catch up on chats and keep listening for new messages", nil)
	cmd.RunE = a.run(func(c *Ctx, cmd *cobra.Command, _ []string) error {
		var idleExit, maxReconnect *time.Duration
		if cmd.Flags().Changed("idle-exit") {
			d := seconds(o.idleExit)
			idleExit = &d
		}
		if cmd.Flags().Changed("max-reconnect") {
			d := seconds(o.maxReconnect)
			maxReconnect = &d
		}
		return c.runSync(o, idleExit, maxReconnect)
	})
	f := cmd.Flags()
	f.BoolVar(&o.once, "once", false, "catch up once over HTTP and exit")
	f.Var(&o.presence, "presence", "quiet = act like a background tab; normal = like an open tab")
	f.BoolVar(&o.noReceipts, "no-receipts", false, "do not send delivery receipts for messages fetched while offline")
	f.Float64Var(&o.idleExit, "idle-exit", 0, "exit after this many seconds without new events")
	f.Float64Var(&o.maxReconnect, "max-reconnect", 0, "give up after being offline this many seconds (default: retry forever)")
	f.IntVar(&o.maxChats, "max-chats", 50, "chats to fetch messages for per catch-up (0 = all)")
	f.IntVar(&o.batch, "batch", 50, "messages per history request")
	f.IntVar(&o.pages, "pages", 3, "max history pages per chat when catching up")
	return cmd
}

func seconds(s float64) time.Duration { return time.Duration(s * float64(time.Second)) }

func (c *Ctx) runSync(o *syncOptions, idleExit, maxReconnect *time.Duration) error {
	if err := c.requireWritable("sync"); err != nil {
		return err
	}
	lock, err := c.storeLock("sync")
	if err != nil {
		return err
	}
	defer lock.Release()
	client, err := c.API()
	if err != nil {
		return err
	}
	conn, err := c.Conn()
	if err != nil {
		return err
	}
	out := syncer.NewEmitter(c.json, false)
	s := syncer.New(conn, client, client.Session.UserID, out, syncer.Options{
		Receipts: !o.noReceipts, Presence: o.presence.value, MaxChats: o.maxChats, BatchSize: o.batch, MaxPages: o.pages,
	})
	if o.once {
		counts, err := s.CatchUp(c.ctx, true)
		if err != nil {
			return err
		}
		if !c.json {
			c.println(fmt.Sprintf("Synced %d chats (%d fetched), %d new messages, %d contacts.", counts.Chats, counts.ChatsFetched, counts.Messages, counts.Contacts))
		}
		return nil
	}
	if stop, err := s.ServeControl(c.ctx, c.store.ControlPath()); err != nil {
		out.Status(fmt.Sprintf("other commands will open their own relay connection (%v)", err))
	} else {
		defer stop()
	}
	reason, err := s.Follow(c.ctx, idleExit, maxReconnect)
	if err != nil {
		return err
	}
	out.Emit("stopped", "reason", reason)
	if !c.json {
		c.status(fmt.Sprintf("sync stopped (%s)", reason))
	}
	return nil
}

func (a *app) historyCommand() *cobra.Command {
	var chat string
	var pages, batch int
	backfill := leaf("backfill", "fetch older messages for one chat from Hike's server", a.run(func(c *Ctx, _ *cobra.Command, _ []string) error {
		return c.runBackfill(chat, pages, batch)
	}))
	f := backfill.Flags()
	f.StringVar(&chat, "chat", "", "chat name, phone number or id")
	f.IntVar(&pages, "pages", 5, "pages to fetch this run")
	f.IntVar(&batch, "batch", 50, "messages per page")
	required(backfill, "chat")
	return a.group("history", "fetch older messages", backfill)
}

func (c *Ctx) runBackfill(spec string, pages, batch int) error {
	if err := c.requireWritable("history backfill"); err != nil {
		return err
	}
	chatID, name, err := c.resolveChat(spec, false)
	if err != nil {
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
	opts := syncer.DefaultOptions()
	opts.BatchSize = batch
	s := syncer.New(conn, client, client.Session.UserID, syncer.NewEmitter(c.json, true), opts)
	added, done, err := s.BackfillChat(c.ctx, chatID, pages)
	if err != nil {
		return err
	}
	if _, err := s.ResolveNames(c.ctx); err != nil {
		return err
	}
	c.output(pyjson.O("chat", chatID, "name", name, "new_messages", added, "complete", done), func() {
		more := " Run again for more."
		if done {
			more = " Reached the start of the chat."
		}
		c.println(fmt.Sprintf("%s: %d older messages added.%s", name, added, more))
	})
	return nil
}
