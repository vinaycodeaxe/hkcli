// Package auth implements the login flows Hike Web uses.
//
// QR: open wss://session.hikeapp.com/session/v1/qr-login?webDeviceId=..&appId=hike-messenger.
// The server sends {"type":"QR_GENERATED","qrToken":..,"expiresInSeconds":..}; the
// QR encodes qrToken as-is. Scanning it in the Hike phone app (Settings > Web QR
// Login) makes the server send {"type":"LOGIN_CONFIRMED","accessToken",
// "refreshToken","userId","profile"}. The client pings every 20 s and asks for a
// fresh QR 20 s before expiry.
//
// OTP: POST /auth/v1/initiate then POST /auth/v1/verify with deviceType WEB.
package auth

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"rsc.io/qr"

	"github.com/vinaycodeaxe/hkcli/internal/api"
	"github.com/vinaycodeaxe/hkcli/internal/config"
	"github.com/vinaycodeaxe/hkcli/internal/protocol"
	"github.com/vinaycodeaxe/hkcli/internal/pyjson"
)

const (
	pingEvery     = 20 * time.Second
	refreshBefore = 20 * time.Second
)

var fatalCloseCodes = map[int]bool{1008: true, 4001: true, 4003: true}

// errConnectionLost is how a dropped TCP connection is reported in status lines.
var errConnectionLost = errors.New("Connection to remote host was lost.")

// LoginError means the login did not complete.
type LoginError struct{ Msg string }

func (e *LoginError) Error() string { return e.Msg }

// QRLoginURL is the login socket for this web device.
func QRLoginURL(deviceID string) string {
	return config.SessionWSBase() + "/session/v1/qr-login?webDeviceId=" + api.Seg(deviceID) + "&appId=" + api.Seg(config.AppID)
}

// QRLogin runs the QR handshake until the phone confirms and returns the
// LOGIN_CONFIRMED message. onQR receives each code and its lifetime in seconds.
func QRLogin(ctx context.Context, deviceID string, timeout time.Duration, onQR func(token string, seconds int64), onStatus func(string)) (pyjson.Object, error) {
	deadline := time.Now().Add(timeout)
	header := http.Header{}
	header.Set("Origin", config.WebOrigin)
	header.Set("User-Agent", config.UserAgent())
	dialer := websocket.Dialer{
		Proxy:            http.ProxyFromEnvironment,
		HandshakeTimeout: 15 * time.Second,
		NetDialContext:   (&net.Dialer{Timeout: 15 * time.Second}).DialContext,
	}
	for time.Now().Before(deadline) {
		ws, _, err := dialer.DialContext(ctx, QRLoginURL(deviceID), header)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			onStatus(fmt.Sprintf("cannot reach the Hike login server (%v); retrying", err))
			if err := sleep(ctx, 2*time.Second); err != nil {
				return nil, err
			}
			continue
		}
		result, err := qrSession(ctx, ws, deadline, onQR, onStatus)
		ws.Close()
		if result != nil || err != nil {
			return result, err
		}
		if time.Until(deadline) > 2*time.Second {
			if err := sleep(ctx, 2*time.Second); err != nil {
				return nil, err
			}
		}
	}
	return nil, &LoginError{"timed out waiting for the QR code to be scanned"}
}

func sleep(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

type frame struct {
	data []byte
	err  error
}

// qrSession handles one login socket; (nil, nil) means reconnect.
func qrSession(ctx context.Context, ws *websocket.Conn, deadline time.Time, onQR func(string, int64), onStatus func(string)) (pyjson.Object, error) {
	frames := make(chan frame)
	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			_, data, err := ws.ReadMessage()
			select {
			case frames <- frame{data, err}:
			case <-done:
				return
			}
			if err != nil {
				return
			}
		}
	}()
	send := func(kind string) error {
		return ws.WriteMessage(websocket.TextMessage, []byte(`{"type": "`+kind+`"}`))
	}
	lastPing := time.Now()
	var expiresAt time.Time
	refreshSent := false
	for time.Now().Before(deadline) {
		now := time.Now()
		var err error
		if now.Sub(lastPing) >= pingEvery {
			err = send("PING")
			lastPing = now
		}
		if err == nil && !expiresAt.IsZero() && !refreshSent && expiresAt.Sub(now) <= refreshBefore {
			err = send("REFRESH_QR")
			refreshSent = true
		}
		var f frame
		if err == nil {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Second):
				continue
			case f = <-frames:
				err = f.err
			}
		}
		if err != nil {
			var ce *websocket.CloseError
			if errors.As(err, &ce) && ce.Code != websocket.CloseAbnormalClosure {
				code := strconv.Itoa(ce.Code)
				if ce.Code == websocket.CloseNoStatusReceived {
					code = "None"
				}
				if fatalCloseCodes[ce.Code] {
					return nil, &LoginError{"Hike refused the login session (close code " + code + ")"}
				}
				onStatus("login server closed the connection (code " + code + "); reconnecting")
				return nil, nil
			}
			if errors.As(err, &ce) {
				err = errConnectionLost
			}
			onStatus(fmt.Sprintf("login connection dropped (%v); reconnecting", err))
			return nil, nil
		}
		v, jerr := pyjson.Loads(f.data)
		if jerr != nil {
			continue
		}
		msg, _ := v.(pyjson.Object)
		switch msg.Get("type") {
		case "QR_GENERATED":
			token := msg.Get("qrToken")
			if !pyjson.Truthy(token) {
				continue
			}
			seconds := int64(120)
			if n, ok := protocol.AsInt(msg.Get("expiresInSeconds")); ok && n != 0 {
				seconds = n
			}
			expiresAt = time.Now().Add(time.Duration(seconds) * time.Second)
			refreshSent = false
			onQR(pyjson.Str(token), seconds)
		case "QR_EXPIRED":
			onStatus("QR code expired; asking for a new one")
			if err := send("REFRESH_QR"); err != nil {
				onStatus(fmt.Sprintf("login connection dropped (%v); reconnecting", err))
				return nil, nil
			}
			expiresAt, refreshSent = time.Time{}, true
		case "LOGIN_CONFIRMED":
			if !pyjson.Truthy(msg.Get("accessToken")) || !pyjson.Truthy(msg.Get("refreshToken")) || msg.Get("userId") == nil {
				return nil, &LoginError{"Hike confirmed the login but did not send tokens"}
			}
			return msg, nil
		case "LOGIN_FAILED":
			return nil, &LoginError{pyjson.Str(pyjson.Or(msg.Get("message"), "Hike rejected the login"))}
		}
	}
	return nil, nil
}

// OTPLogin texts a code to the number and asks readOTP for it (up to three
// tries); it returns the verify response's data with the tokens.
func OTPLogin(ctx context.Context, client *api.Client, deviceID, countryCode, mobile string, readOTP func(expires int64) (string, error)) (pyjson.Object, error) {
	started, err := client.OTPInitiate(ctx, countryCode, mobile, deviceID)
	if err != nil {
		return nil, err
	}
	data := started.Obj("data")
	if !successful(started) || !pyjson.Truthy(data.Get("challengeId")) {
		return nil, &LoginError{pyjson.Str(pyjson.Or(started.Get("message"), "Hike could not send the OTP"))}
	}
	expires, _ := protocol.AsInt(data.Get("expiresInSeconds"))
	for attempt := 0; attempt < 3; attempt++ {
		code, err := readOTP(expires)
		if err != nil {
			return nil, err
		}
		if code = pyjson.Strip(code); code == "" {
			return nil, &LoginError{"no OTP entered"}
		}
		verified, err := client.OTPVerify(ctx, pyjson.Str(data.Get("challengeId")), code, deviceID)
		if err != nil {
			var he *api.Error
			if errors.As(err, &he) && he.Status != 0 && he.Status < 500 && attempt < 2 {
				fmt.Fprintf(os.Stderr, "%v; try again\n", err)
				continue
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, &LoginError{err.Error()}
		}
		if successful(verified) && pyjson.Truthy(verified.Obj("data").Get("accessToken")) {
			return verified.Obj("data"), nil
		}
		reason := pyjson.Str(pyjson.Or(verified.Get("message"), "invalid OTP"))
		if attempt == 2 {
			return nil, &LoginError{reason}
		}
		fmt.Fprintf(os.Stderr, "%s; try again\n", reason)
	}
	return nil, &LoginError{"invalid OTP"}
}

// successful is false only when a response says "success": false (or another falsy value).
func successful(response pyjson.Object) bool {
	v, ok := response.Lookup("success")
	return !ok || pyjson.Truthy(v)
}

// ---- QR rendering ------------------------------------------------------------

// RenderQR draws the code with half blocks, two modules of quiet zone around
// it. With ANSI it forces black-on-white so it scans on dark terminals too.
func RenderQR(token string, ansi bool) (string, error) {
	code, err := qr.Encode(token, qr.H)
	if err != nil {
		return "", err
	}
	const border = 2
	size := code.Size + 2*border
	black := func(x, y int) bool { return code.Black(x-border, y-border) }
	var lines []string
	for y := 0; y < size; y += 2 {
		var b strings.Builder
		if ansi {
			b.WriteString("\x1b[30;107m")
		}
		for x := 0; x < size; x++ {
			top, bottom := black(x, y), y+1 < size && black(x, y+1)
			b.WriteString(halfBlock(top, bottom, ansi))
		}
		if ansi {
			b.WriteString("\x1b[0m")
		}
		lines = append(lines, b.String())
	}
	return strings.Join(lines, "\n"), nil
}

func halfBlock(top, bottom, ansi bool) string {
	if !ansi {
		// Without colours, draw light modules so the code reads correctly on a dark background.
		top, bottom = !top, !bottom
	}
	switch {
	case top && bottom:
		return "█"
	case top:
		return "▀"
	case bottom:
		return "▄"
	}
	return " "
}

// SaveQRPNG writes the code as a PNG (8 pixels per module, 4 modules of border).
func SaveQRPNG(token, path string) error {
	code, err := qr.Encode(token, qr.H)
	if err != nil {
		return err
	}
	return os.WriteFile(path, code.PNG(), 0o666)
}
