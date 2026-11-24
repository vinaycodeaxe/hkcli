package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/vinaycodeaxe/hkcli/internal/media"
	"github.com/vinaycodeaxe/hkcli/internal/protocol"
	"github.com/vinaycodeaxe/hkcli/internal/pyjson"
	"github.com/vinaycodeaxe/hkcli/internal/store"
)

func (a *app) sendCommand() *cobra.Command {
	var textTo, message, textReply string
	text := leaf("text", "send a text message", a.run(func(c *Ctx, _ *cobra.Command, _ []string) error {
		return c.runSendText(textTo, message, textReply)
	}))
	f := text.Flags()
	f.StringVar(&textTo, "to", "", "name, phone number (+country code) or id")
	f.StringVar(&message, "message", "", "text to send; use - to read it from stdin")
	f.StringVar(&textReply, "reply-to", "", "message id to reply to")
	required(text, "to", "message")

	var o sendFileOptions
	file := leaf("file", "send a file as a document", a.run(func(c *Ctx, _ *cobra.Command, _ []string) error {
		return c.runSendFile(o)
	}))
	f = file.Flags()
	f.StringVar(&o.to, "to", "", "name, phone number (+country code) or id")
	f.StringVar(&o.file, "file", "", "path of the file to send")
	f.StringVar(&o.caption, "caption", "", "optional caption")
	f.StringVar(&o.mime, "mime", "", "override the detected MIME type")
	f.StringVar(&o.replyTo, "reply-to", "", "message id to reply to")
	required(file, "to", "file")
	return a.group("send", "send messages and files", text, file)
}

// messageText returns the text to send; "-" reads it from stdin.
func (c *Ctx) messageText(value string) (string, error) {
	text := value
	if value == "-" {
		data, err := c.readStdin(func(r *bufio.Reader) (string, error) {
			b, err := io.ReadAll(r)
			return string(b), err
		})
		if err != nil {
			return "", err
		}
		text = strings.ReplaceAll(strings.ReplaceAll(data, "\r\n", "\n"), "\r", "\n") // text-mode newlines
		text = strings.TrimRight(text, "\n")
	}
	if pyjson.Strip(text) == "" {
		return "", fail("invalid_input", "message is empty")
	}
	return text, nil
}

func (c *Ctx) runSendText(to, message, replyTo string) error {
	if err := c.requireWritable("send text"); err != nil {
		return err
	}
	text, err := c.messageText(message)
	if err != nil {
		return err
	}
	session, err := c.Session()
	if err != nil {
		return err
	}
	chatID, name, err := c.resolveChat(to, true)
	if err != nil {
		return err
	}
	reply, err := c.replyInfo(chatID, replyTo)
	if err != nil {
		return err
	}
	packet := protocol.BuildText(session.UserID, chatID, text, reply)
	serverID, via, err := c.sendPacket(packet)
	if err != nil {
		return err
	}
	if err := c.storeSent(packet, serverID); err != nil {
		return err
	}
	data := pyjson.O("to", chatID, "name", name, "client_id", packet.Get("i"), "server_id", nullable(serverID), "ts", packet.Get("ts"), "via", via)
	c.output(data, func() {
		c.println(fmt.Sprintf("Sent to %s (message id %s).", name, pyjson.Str(pyjson.Or(nullable(serverID), packet.Get("i")))))
	})
	return nil
}

type sendFileOptions struct {
	to, file, caption, mime, replyTo string
}

func (c *Ctx) runSendFile(o sendFileOptions) error {
	if err := c.requireWritable("send file"); err != nil {
		return err
	}
	path := filepath.Clean(store.ExpandUser(o.file))
	if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
		return fail("not_found", "file not found: %s", path)
	}
	client, err := c.API()
	if err != nil {
		return err
	}
	me := client.Session.UserID
	chatID, name, err := c.resolveChat(o.to, true)
	if err != nil {
		return err
	}
	reply, err := c.replyInfo(chatID, o.replyTo)
	if err != nil {
		return err
	}
	base := filepath.Base(path)
	mime := o.mime
	if mime == "" {
		mime = media.GuessMIME(base)
	}
	if !c.json {
		if info, err := os.Stat(path); err == nil {
			c.status(fmt.Sprintf("Uploading %s (%d bytes) ...", base, info.Size()))
		}
	}
	fileID, err := media.Upload(c.ctx, client, path, mime)
	if err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	packet := protocol.BuildFile(me, chatID, protocol.File{ID: fileID, Name: base, MIME: mime, Caption: o.caption, Size: info.Size()}, reply)
	serverID, via, err := c.sendPacket(packet)
	if err != nil {
		return err
	}
	if err := c.storeSent(packet, serverID); err != nil {
		return err
	}
	data := pyjson.O("to", chatID, "name", name, "client_id", packet.Get("i"), "server_id", nullable(serverID), "file_id", fileID, "ts", packet.Get("ts"), "via", via)
	c.output(data, func() {
		c.println(fmt.Sprintf("Sent %s to %s (message id %s).", base, name, pyjson.Str(pyjson.Or(nullable(serverID), packet.Get("i")))))
	})
	return nil
}
