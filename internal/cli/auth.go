package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/vinaycodeaxe/hkcli/internal/api"
	"github.com/vinaycodeaxe/hkcli/internal/auth"
	"github.com/vinaycodeaxe/hkcli/internal/db"
	"github.com/vinaycodeaxe/hkcli/internal/phone"
	"github.com/vinaycodeaxe/hkcli/internal/protocol"
	"github.com/vinaycodeaxe/hkcli/internal/pyjson"
	"github.com/vinaycodeaxe/hkcli/internal/store"
	"github.com/vinaycodeaxe/hkcli/internal/syncer"
)

type authOptions struct {
	phone    string
	qrFormat choice
	timeout  float64
	noSync   bool
	force    bool
	maxChats int
	batch    int
}

func (a *app) authCommand() *cobra.Command {
	o := &authOptions{qrFormat: choice{value: "terminal", allowed: []string{"terminal", "text", "png"}}}
	cmd := &cobra.Command{
		Use:   "auth [status|logout]",
		Short: "log in by scanning a QR code with the Hike app (or --phone for an SMS code)",
		Long:  "log in by scanning a QR code with the Hike app (or --phone for an SMS code)",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				return &usageError{invalidChoice("[status|logout]", args[0], []string{"status", "logout"})}
			}
			return nil
		},
		RunE: a.run(func(c *Ctx, _ *cobra.Command, _ []string) error { return c.runAuth(o) }),
	}
	f := cmd.Flags()
	f.StringVar(&o.phone, "phone", "", "log in with an SMS OTP instead of a QR code, e.g. +919876543210")
	f.Var(&o.qrFormat, "qr-format", "how to show the QR code: terminal, text or png")
	f.Float64Var(&o.timeout, "timeout", 300, "seconds to wait for the scan")
	f.BoolVar(&o.noSync, "no-sync", false, "do not sync after logging in")
	f.BoolVar(&o.force, "force", false, "log in again even if a login is saved")
	f.IntVar(&o.maxChats, "max-chats", 50, "chats to fetch messages for after login (0 = all)")
	f.IntVar(&o.batch, "batch", 50, "messages per history request")

	var check bool
	status := leaf("status", "show who is logged in", a.run(func(c *Ctx, _ *cobra.Command, _ []string) error { return c.runAuthStatus(check) }))
	status.Flags().BoolVar(&check, "check", false, "also ask Hike whether the login still works")
	var localOnly bool
	logout := leaf("logout", "log this device out and forget the saved login", a.run(func(c *Ctx, _ *cobra.Command, _ []string) error { return c.runAuthLogout(localOnly) }))
	logout.Flags().BoolVar(&localOnly, "local-only", false, "only delete the saved login; do not tell Hike")
	cmd.AddCommand(status, logout)
	return cmd
}

// moveOtherAccountDB moves a database that belongs to another account aside
// and returns where it went ("" when nothing moved).
func (c *Ctx) moveOtherAccountDB(userID string) (string, error) {
	dbPath := c.store.DBPath()
	if _, err := os.Stat(dbPath); err != nil {
		return "", nil
	}
	probe, err := db.Connect(dbPath)
	if err != nil {
		return "", err
	}
	owner, _, err := probe.GetMeta("user_id")
	probe.Close()
	if err != nil || owner == "" || owner == userID {
		return "", err
	}
	c.close()
	backup := filepath.Join(c.store.Root, fmt.Sprintf("hike.db.%s.%d.bak", safeFilename(owner), protocol.NowMS()))
	if err := os.Rename(dbPath, backup); err != nil {
		return "", err
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(dbPath + suffix); err == nil {
			if err := os.Rename(dbPath+suffix, backup+suffix); err != nil {
				return "", err
			}
		}
	}
	return backup, nil
}

func (c *Ctx) runAuth(o *authOptions) error {
	if err := c.requireWritable("auth"); err != nil {
		return err
	}
	if existing := c.store.LoadSession(); existing != nil && existing.Revoked == "" && !o.force {
		return fail("already_logged_in", "already logged in as %s (%s); run 'hkcli auth logout' first or pass --force", existing.DisplayName(), existing.UserID)
	}
	lock, err := c.storeLock("a login or sync")
	if err != nil {
		return err
	}
	defer lock.Release()
	device, err := c.store.DeviceID()
	if err != nil {
		return err
	}
	var payload pyjson.Object
	method := "qr"
	if o.phone != "" {
		method = "otp"
		var code, national string
		if code, national, err = phone.Split(o.phone); err != nil {
			return fail("invalid_input", "%s", err.Error())
		}
		c.status(fmt.Sprintf("Asking Hike to send an OTP to %s %s ...", code, national))
		readOTP := func(expires int64) (string, error) {
			hint := ""
			if expires != 0 {
				hint = fmt.Sprintf(" (valid %ds)", expires)
			}
			fmt.Fprintf(c.stderr, "Enter the OTP you received%s: ", hint)
			return c.readStdin(func(r *bufio.Reader) (string, error) { return r.ReadString('\n') })
		}
		payload, err = auth.OTPLogin(c.ctx, api.New(c.store, nil), device, code, national, readOTP)
	} else {
		payload, err = auth.QRLogin(c.ctx, device, time.Duration(o.timeout*float64(time.Second)), c.showQR(o.qrFormat.value, auth.EnableANSI()), c.status)
	}
	if err != nil {
		return err
	}
	userID := protocol.AsStr(payload.Get("userId"))
	if userID == "" {
		return &auth.LoginError{Msg: "Hike did not return a user id"}
	}
	profile := payload.Obj("profile")
	if profile == nil {
		profile = pyjson.Object{}
	}
	session := &store.Session{
		UserID:       userID,
		AccessToken:  pyjson.Str(payload.Get("accessToken")),
		RefreshToken: pyjson.Str(payload.Get("refreshToken")),
		Profile:      profile,
		LoginMethod:  method,
		CreatedAt:    protocol.NowMS(),
	}
	moved, err := c.moveOtherAccountDB(userID)
	if err != nil {
		return err
	}
	if err := c.store.SaveSession(session); err != nil {
		return err
	}
	_ = os.Remove(c.store.QRPNGPath())
	conn, err := c.Conn()
	if err != nil {
		return err
	}
	if err := conn.SetMeta("user_id", userID); err != nil {
		return err
	}
	if err := conn.Commit(); err != nil {
		return err
	}
	c.api = api.New(c.store, session)
	result := pyjson.O(
		"user_id", userID, "name", profile.Get("name"), "phone", profile.Get("phone"), "method", method,
		"store", c.store.Root, "previous_account_db", nullable(moved), "sync", nil,
	)
	if moved != "" {
		c.status("This store belonged to another account; its database was moved to " + moved)
	}
	c.status(fmt.Sprintf("Logged in as %s (%s).", session.DisplayName(), userID))
	var counts *syncer.Counts
	if !o.noSync {
		c.status("Syncing contacts, chats and recent messages ...")
		opts := syncer.DefaultOptions()
		opts.MaxChats, opts.BatchSize = o.maxChats, o.batch
		n, err := syncer.New(conn, c.api, userID, syncer.NewEmitter(false, true), opts).CatchUp(c.ctx, true)
		if err != nil {
			return err
		}
		counts = &n
		result.Set("sync", pyjson.O(n.Pairs()...))
	}
	c.output(result, func() {
		if counts != nil {
			c.println(fmt.Sprintf("Synced %d chats, %d messages, %d contacts.", counts.Chats, counts.Messages, counts.Contacts))
		}
		c.println("Next: 'hkcli chats list', 'hkcli sync' to stay up to date, 'hkcli send text --to <name> --message hi'.")
	})
	return nil
}

// showQR displays each login code in the chosen format and saves it as a PNG.
func (c *Ctx) showQR(format string, ansi bool) func(token string, seconds int64) {
	pngPath := c.store.QRPNGPath()
	return func(token string, seconds int64) {
		_ = auth.SaveQRPNG(token, pngPath)
		fmt.Fprintln(c.stderr)
		switch format {
		case "text":
			c.status("QR text (encode it as a QR code to scan it): " + token)
		case "png":
			c.status("QR image: " + pngPath)
			openFile(pngPath)
		default:
			if art, err := auth.RenderQR(token, ansi); err == nil {
				fmt.Fprintln(c.stderr, art)
			}
			c.status("If it does not scan, open " + pngPath)
		}
		c.status(fmt.Sprintf("On your phone: open Hike > Settings > Web QR Login > scan this code (new code in about %ds).", max(seconds-20, 1)))
	}
}

func (c *Ctx) runAuthStatus(check bool) error {
	session := c.store.LoadSession()
	loggedIn := session != nil && session.Revoked == ""
	data := pyjson.O("logged_in", loggedIn, "user_id", nil, "name", nil, "phone", nil, "method", nil, "since", nil, "revoked", nil,
		"store", c.store.Root, "server_check", nil)
	if session != nil {
		data.Set("user_id", session.UserID)
		data.Set("name", session.Profile.Get("name"))
		data.Set("phone", session.Profile.Get("phone"))
		data.Set("method", session.LoginMethod)
		data.Set("since", iso(session.CreatedAt))
		data.Set("revoked", nullable(session.Revoked))
	}
	serverCheck := ""
	if check && loggedIn {
		client, err := c.API()
		if err == nil {
			err = client.CheckLogin(c.ctx)
		}
		var authErr *api.AuthRequiredError
		switch {
		case err == nil:
			serverCheck = "ok"
		case errors.As(err, &authErr):
			loggedIn = false
			serverCheck = err.Error()
			data.Set("logged_in", false)
		default:
			return err
		}
		data.Set("server_check", serverCheck)
	}
	c.output(data, func() {
		switch {
		case loggedIn:
			who := pyjson.Str(pyjson.Or(pyjson.Or(data.Get("name"), data.Get("phone")), data.Get("user_id")))
			c.println(fmt.Sprintf("Logged in as %s (user id %s) via %s since %s.", who, session.UserID, session.LoginMethod, syncer.FmtTime(session.CreatedAt)))
		case session != nil && session.Revoked != "":
			c.println(fmt.Sprintf("Saved login no longer works (%s). Run: hkcli auth --force", session.Revoked))
		default:
			c.println("Not logged in. Run: hkcli auth")
		}
		if serverCheck != "" {
			c.println("Server check: " + serverCheck)
		}
		c.println("Store: " + c.store.Root)
	})
	return nil
}

func (c *Ctx) runAuthLogout(localOnly bool) error {
	if err := c.requireWritable("auth logout"); err != nil {
		return err
	}
	session := c.store.LoadSession()
	if session == nil {
		c.output(pyjson.O("logged_out", false, "server_logout", nil), func() { c.println("Not logged in.") })
		return nil
	}
	server := "skipped"
	if !localOnly && session.Revoked == "" {
		server = "ok"
		if err := api.New(c.store, session).Logout(c.ctx, session.RefreshToken); err != nil {
			if !api.IsHikeError(err) {
				return err
			}
			server = "failed: " + err.Error()
		}
	}
	if _, err := c.store.DeleteSession(); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	data := pyjson.O("logged_out", true, "server_logout", server, "local_history", c.store.DBPath())
	c.output(data, func() {
		c.println(fmt.Sprintf("Logged out (server: %s). Local history is still in %s.", server, c.store.DBPath()))
	})
	return nil
}
