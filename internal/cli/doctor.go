package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/vinaycodeaxe/hkcli/internal/api"
	"github.com/vinaycodeaxe/hkcli/internal/config"
	"github.com/vinaycodeaxe/hkcli/internal/db"
	"github.com/vinaycodeaxe/hkcli/internal/mqtt"
	"github.com/vinaycodeaxe/hkcli/internal/pyjson"
	"github.com/vinaycodeaxe/hkcli/internal/syncer"
)

func (a *app) doctorCommand() *cobra.Command {
	var connect bool
	cmd := leaf("doctor", "check the install, store and login", a.run(func(c *Ctx, _ *cobra.Command, _ []string) error {
		return c.runDoctor(connect)
	}))
	cmd.Flags().BoolVar(&connect, "connect", false, "also test the live API and message relay")
	return cmd
}

func (c *Ctx) runDoctor(connect bool) error {
	var checks []pyjson.Object
	ok := true
	check := func(name string, passed bool, detail string) {
		checks = append(checks, pyjson.O("check", name, "ok", passed, "detail", detail))
		ok = ok && passed
	}
	check("go", true, fmt.Sprintf("%s %s/%s", runtime.Version(), runtime.GOOS, runtime.GOARCH))
	check("sqlite fts5", db.FTS5Available(), "SQLite "+db.SQLiteVersion())

	err := c.store.Ensure()
	if err == nil {
		probe := filepath.Join(c.store.Root, ".doctor")
		if err = os.WriteFile(probe, []byte("ok"), 0o600); err == nil {
			err = os.Remove(probe)
		}
	}
	if err == nil {
		check("store", true, c.store.Root)
	} else {
		check("store", false, fmt.Sprintf("%s: %v", c.store.Root, err))
	}

	conn, err := c.Conn()
	var stats db.Stats
	if err == nil {
		stats, err = conn.Stats()
	}
	if err == nil {
		check("database", true, fmt.Sprintf("%d chats, %d messages, %d contacts", stats.Chats, stats.Messages, stats.Contacts))
	} else {
		check("database", false, err.Error())
	}

	session := c.store.LoadSession()
	switch {
	case session == nil:
		check("login", false, "not logged in (run: hkcli auth)")
	case session.Revoked != "":
		check("login", false, "saved login no longer works: "+session.Revoked)
	default:
		check("login", true, fmt.Sprintf("%s (%s) via %s", session.DisplayName(), session.UserID, session.LoginMethod))
	}
	check("endpoints", true, fmt.Sprintf("api=%s login=%s relay=%s", config.APIBase(), config.SessionWSBase(), config.MQTTWSURL()))

	if connect && session != nil && session.Revoked == "" {
		client, err := c.API()
		if err == nil {
			err = client.CheckLogin(c.ctx)
		}
		switch {
		case err == nil:
			check("api", true, "authenticated request ok")
		case api.IsHikeError(err):
			check("api", false, err.Error())
		default:
			return err
		}
		if err := c.checkRelay(); err == nil {
			check("relay", true, "MQTT login and subscribe ok")
		} else {
			var relayErr *mqtt.Error
			if !errors.As(err, &relayErr) && !api.IsHikeError(err) {
				return err
			}
			check("relay", false, err.Error())
		}
	}

	c.output(pyjson.O("ok", ok, "checks", checks), func() {
		for _, ch := range checks {
			label := "FAIL"
			if ch.Get("ok") == true {
				label = "ok"
			}
			c.println(fmt.Sprintf("[%4s] %s: %s", label, ch.Get("check"), ch.Get("detail")))
		}
	})
	if !ok {
		return exitStatus(exitError)
	}
	return nil
}

func (c *Ctx) checkRelay() error {
	client, err := c.API()
	if err != nil {
		return err
	}
	relay, err := syncer.OpenMQTT(c.ctx, client)
	if err != nil {
		return err
	}
	defer relay.Disconnect()
	_, err = relay.Subscribe(c.ctx, "u:"+client.Session.UserID, 1)
	return err
}
