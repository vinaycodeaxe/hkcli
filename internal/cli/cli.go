// Package cli implements the hkcli command line.
package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/vinaycodeaxe/hkcli/internal/api"
	"github.com/vinaycodeaxe/hkcli/internal/auth"
	"github.com/vinaycodeaxe/hkcli/internal/config"
	"github.com/vinaycodeaxe/hkcli/internal/db"
	"github.com/vinaycodeaxe/hkcli/internal/mqtt"
	"github.com/vinaycodeaxe/hkcli/internal/pyjson"
	"github.com/vinaycodeaxe/hkcli/internal/store"
	"github.com/vinaycodeaxe/hkcli/internal/syncer"
	"github.com/vinaycodeaxe/hkcli/internal/version"
)

// Exit codes.
const (
	exitOK          = 0
	exitError       = 1
	exitUsage       = 2
	exitAuth        = 3
	exitInterrupted = 130
)

// cliError is a failure with a stable error code for --json output.
type cliError struct {
	msg, code string
}

func (e *cliError) Error() string { return e.msg }

func fail(code, format string, args ...any) error {
	return &cliError{msg: fmt.Sprintf(format, args...), code: code}
}

// exitStatus ends a command with an exit code and no error message.
type exitStatus int

func (e exitStatus) Error() string { return fmt.Sprintf("exit status %d", int(e)) }

// usageError is a command-line mistake (exit 2).
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

// Ctx holds what every command needs: output mode, the store and lazily opened
// database and API connections.
type Ctx struct {
	ctx      context.Context
	json     bool
	readOnly bool
	store    *store.Store
	conn     *db.Conn
	api      *api.Client
	stdin    *bufio.Reader
	stdout   io.Writer
	stderr   io.Writer
}

// Conn opens the database on first use.
func (c *Ctx) Conn() (*db.Conn, error) {
	if c.conn == nil {
		if err := c.store.Ensure(); err != nil {
			return nil, err
		}
		conn, err := db.Connect(c.store.DBPath())
		if err != nil {
			return nil, err
		}
		c.conn = conn
	}
	return c.conn, nil
}

// Session returns the saved login, or an auth error.
func (c *Ctx) Session() (*store.Session, error) {
	s := c.store.LoadSession()
	if s == nil {
		return nil, &api.AuthRequiredError{Msg: "not logged in; run: hkcli auth"}
	}
	if s.Revoked != "" {
		return nil, &api.AuthRequiredError{Msg: fmt.Sprintf("the saved login no longer works (%s); run: hkcli auth", s.Revoked)}
	}
	return s, nil
}

// API returns the client for the logged-in user.
func (c *Ctx) API() (*api.Client, error) {
	if c.api == nil {
		s, err := c.Session()
		if err != nil {
			return nil, err
		}
		c.api = api.New(c.store, s)
	}
	return c.api, nil
}

// Me is the logged-in (or last) user id of this store.
func (c *Ctx) Me() (string, error) {
	if s := c.store.LoadSession(); s != nil {
		return s.UserID, nil
	}
	conn, err := c.Conn()
	if err != nil {
		return "", err
	}
	me, _, err := conn.GetMeta("user_id")
	return me, err
}

func (c *Ctx) requireWritable(what string) error {
	if c.readOnly {
		return fail("read_only", "'%s' is blocked by --read-only (HKCLI_READONLY)", what)
	}
	return nil
}

// output prints {"success": true, "data": ...} in JSON mode, else calls human.
func (c *Ctx) output(data any, human func()) {
	if c.json {
		fmt.Fprintln(c.stdout, pyjson.Dumps(pyjson.O("success", true, "data", data, "error", nil)))
	} else {
		human()
	}
}

func (c *Ctx) status(msg string) { fmt.Fprintln(c.stderr, msg) }

func (c *Ctx) println(a ...any) { fmt.Fprintln(c.stdout, a...) }

func (c *Ctx) close() {
	if c.conn != nil {
		c.conn.Close()
		c.conn = nil
	}
}

// readStdin runs read on stdin (one shared buffered reader) and gives up when
// the command is interrupted. EOF is not an error: read returns what it got.
func (c *Ctx) readStdin(read func(*bufio.Reader) (string, error)) (string, error) {
	if c.stdin == nil {
		c.stdin = bufio.NewReader(os.Stdin)
	}
	type result struct {
		text string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		text, err := read(c.stdin)
		if errors.Is(err, io.EOF) {
			err = nil
		}
		done <- result{text, err}
	}()
	select {
	case r := <-done:
		return r.text, r.err
	case <-c.ctx.Done():
		return "", c.ctx.Err()
	}
}

// Main runs hkcli with the given arguments and returns the exit code.
func Main(args []string) int {
	// SIGTERM (service managers, kill; on Windows, closing the console) stops as gracefully as
	// Ctrl+C: a sync then finishes the sends it took on and removes its control endpoint.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		// A second Ctrl+C ends the process at once. Another SIGTERM changes nothing: service
		// managers may repeat it.
		signal.Reset(os.Interrupt)
	}()
	app := &app{ctx: &Ctx{ctx: ctx, stdout: os.Stdout, stderr: os.Stderr}}
	defer app.ctx.close()
	root := app.rootCommand()
	root.SetArgs(args)
	cmd, err := root.ExecuteC()
	return app.exitCode(cmd, err)
}

type app struct {
	ctx      *Ctx
	ran      bool // a command started; later errors are not usage errors
	storeDir string
	json     bool
	readOnly bool
}

func (a *app) exitCode(cmd *cobra.Command, err error) int {
	if err == nil {
		return exitOK
	}
	var status exitStatus
	var usage *usageError
	switch {
	case errors.As(err, &status):
		return int(status)
	case errors.As(err, &usage) || !a.ran:
		if usage == nil || usage.msg != "" {
			fmt.Fprintf(a.ctx.stderr, "usage: %s\n%s: error: %v\n", cmd.UseLine(), cmd.CommandPath(), err)
		}
		return exitUsage
	case a.ctx.ctx.Err() != nil:
		fmt.Fprintln(a.ctx.stderr, "interrupted")
		return exitInterrupted
	}
	code, exit := "error", exitError
	var authErr *api.AuthRequiredError
	var cliErr *cliError
	var loginErr *auth.LoginError
	var hikeErr *api.Error
	var relayErr *mqtt.Error
	var busyErr *syncer.RelayUnavailableError
	var tooLargeErr *syncer.TooLargeError
	switch {
	case errors.As(err, &authErr):
		code, exit = "auth_required", exitAuth
	case errors.As(err, &cliErr):
		code = cliErr.code
	case errors.As(err, &loginErr):
		code = "login_failed"
	case errors.As(err, &hikeErr):
		code = hikeErr.Code
		if code == "" {
			code = "hike_error"
		}
	case errors.As(err, &relayErr):
		code = "relay_error"
	case errors.As(err, &busyErr):
		code = "relay_unavailable" // nothing was sent
	case errors.As(err, &tooLargeErr):
		code = "invalid_input"
	}
	if a.ctx.json {
		fmt.Fprintln(a.ctx.stdout, pyjson.Dumps(pyjson.O("success", false, "data", nil, "error", pyjson.O("code", code, "message", err.Error()))))
	} else {
		fmt.Fprintf(a.ctx.stderr, "error: %v\n", err)
	}
	return exit
}

// run wraps a command body: it prepares the Ctx from the global flags first.
func (a *app) run(body func(c *Ctx, cmd *cobra.Command, args []string) error) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		a.ran = true
		c := a.ctx
		c.json = a.json
		c.readOnly = a.readOnly || config.ReadOnlyFromEnv()
		c.store = store.New(a.storeDir)
		return body(c, cmd, args)
	}
}

// choice is a string flag limited to a few values.
type choice struct {
	value   string
	allowed []string
}

func (c *choice) String() string { return c.value }
func (c *choice) Type() string   { return "string" }
func (c *choice) Set(v string) error {
	for _, a := range c.allowed {
		if v == a {
			c.value = v
			return nil
		}
	}
	return fmt.Errorf("invalid choice: %s (choose from %s)", pyjson.Repr(v), quoteAll(c.allowed))
}

// quoteAll lists names for error messages: 'a', 'b'.
func quoteAll(names []string) string {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = pyjson.Repr(n)
	}
	return strings.Join(quoted, ", ")
}

// group is a command that only holds subcommands; it rejects a missing or
// unknown subcommand with a usage error.
func (a *app) group(use, short string, children ...*cobra.Command) *cobra.Command {
	names := make([]string, len(children))
	for i, child := range children {
		names[i] = child.Name()
	}
	metavar := "<" + strings.Join(names, "|") + ">"
	cmd := &cobra.Command{
		Use:   use + " " + metavar,
		Short: short,
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return &usageError{"the following arguments are required: " + metavar}
			}
			return &usageError{invalidChoice(metavar, args[0], names)}
		},
	}
	cmd.AddCommand(children...)
	return cmd
}

func invalidChoice(metavar, got string, names []string) string {
	return fmt.Sprintf("argument %s: invalid choice: %s (choose from %s)", metavar, pyjson.Repr(got), quoteAll(names))
}

func leaf(use, help string, run func(*cobra.Command, []string) error) *cobra.Command {
	return &cobra.Command{Use: use, Short: help, Long: help, Args: noArgs, RunE: run}
}

func noArgs(_ *cobra.Command, args []string) error {
	if len(args) > 0 {
		return &usageError{"unrecognized arguments: " + strings.Join(args, " ")}
	}
	return nil
}

func required(cmd *cobra.Command, names ...string) {
	for _, n := range names {
		_ = cmd.MarkFlagRequired(n)
	}
}

func (a *app) rootCommand() *cobra.Command {
	commands := []*cobra.Command{
		a.authCommand(), a.syncCommand(), a.chatsCommand(), a.messagesCommand(), a.historyCommand(),
		a.sendCommand(), a.contactsCommand(), a.groupsCommand(), a.mediaCommand(), a.doctorCommand(),
	}
	names := make([]string, len(commands))
	for i, c := range commands {
		names[i] = c.Name()
	}
	root := &cobra.Command{
		Use:           "hkcli",
		Short:         "Unofficial Hike Messenger CLI",
		Long:          "Unofficial Hike Messenger CLI: links as a Hike Web device, keeps a searchable local copy of your chats, and sends messages.",
		Version:       version.Version,
		SilenceErrors: true,
		SilenceUsage:  true,
		Args:          cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				_ = cmd.Help()
				return &usageError{}
			}
			return &usageError{invalidChoice("<command>", args[0], names)}
		},
	}
	root.SetVersionTemplate("hkcli {{.Version}}\n")
	root.Flags().Bool("version", false, "show program's version number and exit")
	pf := root.PersistentFlags()
	pf.StringVar(&a.storeDir, "store", "", "store directory (default: ~/.hkcli, or $HKCLI_STORE)")
	pf.BoolVar(&a.json, "json", false, "print JSON ({success,data,error}); sync prints NDJSON events")
	pf.BoolVar(&a.readOnly, "read-only", false, "refuse anything that changes Hike or the local store (or set HKCLI_READONLY=1)")
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return &usageError{err.Error()} })
	root.CompletionOptions.DisableDefaultCmd = true
	root.SetHelpCommand(&cobra.Command{Use: "no-help", Hidden: true}) // no "help" command; -h/--help only
	root.AddCommand(commands...)
	return root
}
