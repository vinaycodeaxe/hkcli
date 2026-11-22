// Package api is the Hike REST client (https://api-ext.hikeapp.com), mirroring
// Hike Web's fetch wrapper.
//
// Hike Web sends the raw access token in the Authorization header (no "Bearer"),
// plus X-User-Id, X-App-Id, X-App-Version and X-Build-Number. A 401 triggers one
// POST /auth/v1/refresh and a retry.
package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/vinaycodeaxe/hkcli/internal/config"
	"github.com/vinaycodeaxe/hkcli/internal/pyjson"
	"github.com/vinaycodeaxe/hkcli/internal/store"
)

// Error is a failed Hike request.
type Error struct {
	Msg    string
	Status int    // HTTP status; 0 when no response arrived
	Code   string // Hike's error code, if it sent one
}

func (e *Error) Error() string { return e.Msg }

// AuthRequiredError means there is no usable login: never logged in, logged
// out, or the refresh token was rejected.
type AuthRequiredError struct{ Msg string }

func (e *AuthRequiredError) Error() string { return e.Msg }

// IsHikeError reports whether err is an Error or an AuthRequiredError.
func IsHikeError(err error) bool {
	var he *Error
	var ar *AuthRequiredError
	return errors.As(err, &he) || errors.As(err, &ar)
}

// Client talks to Hike's REST API as the logged-in web device.
type Client struct {
	Store   *store.Store
	Session *store.Session
	http    *http.Client
}

// New returns a client; session may be nil for the login endpoints.
func New(st *store.Store, session *store.Session) *Client {
	return &Client{Store: st, Session: session, http: &http.Client{Timeout: 30 * time.Second}}
}

// Param is one query parameter; order is kept.
type Param struct{ Key, Value string }

func encodeParams(params []Param) string {
	if len(params) == 0 {
		return ""
	}
	parts := make([]string, len(params))
	for i, p := range params {
		parts[i] = url.QueryEscape(p.Key) + "=" + url.QueryEscape(p.Value)
	}
	return "?" + strings.Join(parts, "&")
}

// Seg percent-encodes one path segment; only letters, digits and "_.-~" stay as they are.
func Seg(value string) string {
	var b strings.Builder
	for i := 0; i < len(value); i++ {
		c := value[i]
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || strings.IndexByte("_.-~", c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func (c *Client) send(ctx context.Context, method, path string, body any, params []Param, auth bool) (any, error) {
	var reader io.Reader
	if body != nil {
		reader = strings.NewReader(pyjson.DumpsASCII(body))
	}
	req, err := http.NewRequestWithContext(ctx, method, config.APIBase()+path+encodeParams(params), reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-App-Id", config.AppID)
	req.Header.Set("X-App-Version", config.AppVersion())
	req.Header.Set("X-Build-Number", config.BuildNumber())
	req.Header.Set("User-Agent", config.UserAgent())
	if auth && c.Session != nil {
		req.Header.Set("Authorization", c.Session.AccessToken)
		req.Header.Set("X-User-Id", c.Session.UserID)
	}
	resp, err := c.http.Do(req)
	var data []byte
	if err == nil {
		data, err = io.ReadAll(resp.Body)
		resp.Body.Close()
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &Error{Msg: fmt.Sprintf("network error talking to Hike (%s %s): %v", method, path, err)}
	}
	payload := parseJSON(data)
	if resp.StatusCode >= 400 {
		e := &Error{Msg: fmt.Sprintf("HTTP %d", resp.StatusCode), Status: resp.StatusCode}
		if obj, ok := payload.(pyjson.Object); ok {
			if m, ok := obj.Get("message").(string); ok {
				e.Msg = m
			}
			e.Code, _ = obj.Get("code").(string)
		}
		e.Msg = fmt.Sprintf("%s %s failed: %s", method, path, e.Msg)
		return nil, e
	}
	return payload, nil
}

// parseJSON decodes a response body, or returns nil when it is empty or not JSON.
func parseJSON(data []byte) any {
	data = bytes.TrimPrefix(data, []byte("\ufeff"))
	if !utf8.Valid(data) || pyjson.Strip(string(data)) == "" {
		return nil
	}
	v, err := pyjson.Loads(data)
	if err != nil {
		return nil
	}
	return v
}

func (c *Client) request(ctx context.Context, method, path string, body any, params ...Param) (any, error) {
	if c.Session == nil {
		return nil, &AuthRequiredError{"not logged in; run: hkcli auth"}
	}
	payload, err := c.send(ctx, method, path, body, params, true)
	var he *Error
	if err == nil || !errors.As(err, &he) || he.Status != http.StatusUnauthorized {
		return payload, err
	}
	if err := c.Refresh(ctx); err != nil {
		return nil, err
	}
	return c.send(ctx, method, path, body, params, true)
}

// Refresh rotates the access token. It is serialized across processes so two
// hkcli processes never spend the same refresh token.
func (c *Client) Refresh(ctx context.Context) error {
	if c.Session == nil {
		return &AuthRequiredError{"not logged in; run: hkcli auth"}
	}
	stale := c.Session.AccessToken
	lock := store.NewFileLock(c.Store.RefreshLockPath())
	ok, err := lock.Acquire(ctx, 30*time.Second)
	if err != nil {
		return err
	}
	if !ok {
		return &Error{Msg: "timed out waiting for another hkcli process to refresh the login"}
	}
	defer lock.Release()
	current := c.Store.LoadSession()
	if current == nil {
		return &AuthRequiredError{"logged out; run: hkcli auth"}
	}
	c.Session = current
	if current.Revoked != "" {
		return &AuthRequiredError{fmt.Sprintf("saved login is no longer valid (%s); run: hkcli auth", current.Revoked)}
	}
	if current.AccessToken != stale {
		return nil // another process already refreshed
	}
	payload, err := c.send(ctx, "POST", "/auth/v1/refresh", pyjson.O("refreshToken", current.RefreshToken, "deviceType", "WEB"), nil, true)
	if err != nil {
		var he *Error
		if errors.As(err, &he) && (he.Status == http.StatusUnauthorized || he.Status == http.StatusForbidden) {
			current.Revoked = fmt.Sprintf("refresh rejected with HTTP %d", he.Status)
			if err := c.Store.SaveSession(current); err != nil {
				return err
			}
			return &AuthRequiredError{"Hike rejected the saved login (the web device may have been logged out from the phone). Run: hkcli auth"}
		}
		return err
	}
	data := dataOf(payload)
	if !pyjson.Truthy(data.Get("accessToken")) {
		return &Error{Msg: "token refresh response did not include an access token"}
	}
	current.AccessToken = pyjson.Str(data.Get("accessToken"))
	if refresh := data.Get("refreshToken"); pyjson.Truthy(refresh) {
		current.RefreshToken = pyjson.Str(refresh)
	}
	current.RefreshedAt = store.NowMS()
	return c.Store.SaveSession(current)
}

// dataOf returns the "data" object of a response, or nil.
func dataOf(payload any) pyjson.Object {
	obj, _ := payload.(pyjson.Object)
	return obj.Obj("data")
}

func objectOf(payload any) pyjson.Object {
	obj, _ := payload.(pyjson.Object)
	return obj
}

func listOf(v any) []any {
	list, _ := v.([]any)
	return list
}

// ---- auth --------------------------------------------------------------

// OTPInitiate asks Hike to text a login code.
func (c *Client) OTPInitiate(ctx context.Context, countryCode, mobile, deviceID string) (pyjson.Object, error) {
	payload, err := c.send(ctx, "POST", "/auth/v1/initiate", pyjson.O(
		"mobileNumber", mobile, "countryCode", countryCode, "deviceType", "WEB", "deviceFingerprint", deviceID,
	), nil, false)
	return objectOf(payload), err
}

// OTPVerify exchanges the texted code for tokens.
func (c *Client) OTPVerify(ctx context.Context, challengeID, otp, deviceID string) (pyjson.Object, error) {
	payload, err := c.send(ctx, "POST", "/auth/v1/verify", pyjson.O(
		"challengeId", challengeID, "otp", otp, "deviceType", "WEB", "deviceId", deviceID,
		"name", "", "deviceOsVersion", config.DeviceDescription(),
	), nil, false)
	return objectOf(payload), err
}

// Logout ends this web device's login on Hike's side.
func (c *Client) Logout(ctx context.Context, refreshToken string) error {
	_, err := c.send(ctx, "POST", "/auth/v1/logout", pyjson.O("refreshToken", refreshToken, "logoutAllDevices", false), nil, true)
	return err
}

// CheckLogin makes a cheap authenticated request to confirm the login still works.
// It reloads the user's own profile, which is how Hike Web refreshes its own settings.
func (c *Client) CheckLogin(ctx context.Context) error {
	if c.Session == nil {
		return &AuthRequiredError{Msg: "not logged in; run: hkcli auth"}
	}
	_, err := c.Profiles(ctx, []string{c.Session.UserID})
	return err
}

// ---- conversations and messages -----------------------------------------

// Conversations lists the chats Hike Web shows.
func (c *Client) Conversations(ctx context.Context) ([]any, error) {
	payload, err := c.request(ctx, "GET", "/msg-history/v1/conversations/web", nil)
	return listOf(dataOf(payload).Get("items")), err
}

// Messages fetches one history page, newest first. cursor and the returned
// next cursor are Hike's raw values (nil at the end of the history).
func (c *Client) Messages(ctx context.Context, conversationID string, batchSize int, cursor any) (items []any, next any, err error) {
	params := []Param{{"batchSize", fmt.Sprint(batchSize)}}
	if pyjson.Truthy(cursor) {
		params = append(params, Param{"startMessageId", pyjson.Str(cursor)})
	}
	payload, err := c.request(ctx, "GET", "/msg-history/v1/messages/fetch/"+Seg(conversationID), nil, params...)
	if err != nil {
		return nil, nil, err
	}
	data := dataOf(payload)
	items = listOf(data.Get("items"))
	page := data.Obj("page")
	if len(items) > 0 && float64(len(items)) >= number(page.Get("limit")) {
		next = page.Get("nextCursor")
	}
	return items, next, nil
}

func number(v any) float64 {
	switch x := v.(type) {
	case int64:
		return float64(x)
	case float64:
		return x
	case bool:
		if x {
			return 1
		}
	}
	return 0
}

// ---- people and groups ----------------------------------------------------

// ContactsPage fetches contacts changed since syncedAt.
func (c *Client) ContactsPage(ctx context.Context, syncedAt int64, cursor any) (contacts []any, next, serverTime any, err error) {
	params := []Param{{"batchSize", "200"}, {"syncedAt", fmt.Sprint(syncedAt)}}
	if pyjson.Truthy(cursor) {
		params = append(params, Param{"nextCursor", pyjson.Str(cursor)})
	}
	payload, err := c.request(ctx, "GET", "/contact/v1/contacts/getcontacts", nil, params...)
	if err != nil {
		return nil, nil, nil, err
	}
	data := dataOf(payload)
	return listOf(data.Get("contacts")), data.Get("nextCursor"), data.Get("syncedAt"), nil
}

// Profiles looks up names and numbers for user ids.
func (c *Client) Profiles(ctx context.Context, userIDs []string) ([]any, error) {
	payload, err := c.request(ctx, "POST", "/auth/v1/user/profiles/sync", pyjson.O("userIds", userIDs, "newUserIds", userIDs, "syncedAt", 0))
	return listOf(dataOf(payload).Get("profiles")), err
}

// ProfilesByPhone finds Hike users by phone number (+E.164).
func (c *Client) ProfilesByPhone(ctx context.Context, phones []string) ([]any, error) {
	payload, err := c.request(ctx, "POST", "/auth/v1/user/profiles/by-phone", pyjson.O("phones", phones, "appId", config.AppID))
	return listOf(objectOf(payload).Get("data")), err
}

// Group returns a group's details and members.
func (c *Client) Group(ctx context.Context, groupID string) (pyjson.Object, error) {
	payload, err := c.request(ctx, "GET", "/group/v1/groups/"+Seg(groupID), nil)
	return dataOf(payload), err
}

// ---- media ----------------------------------------------------------------

// MediaDownloadInfo returns signed links to byte-range chunks of a file.
func (c *Client) MediaDownloadInfo(ctx context.Context, fileID string) (pyjson.Object, error) {
	payload, err := c.request(ctx, "GET", "/media/v1/"+Seg(fileID)+"/download-url", nil)
	return dataOf(payload), err
}

// UploadInitiate starts an upload and returns the chunk plan.
func (c *Client) UploadInitiate(ctx context.Context, fileName, mimeType string, size int64) (pyjson.Object, error) {
	payload, err := c.request(ctx, "POST", "/media/v1/upload/initiate", pyjson.O("fileName", fileName, "mimeType", mimeType, "fileSizeBytes", size))
	return dataOf(payload), err
}

// UploadComplete finishes an upload and returns the new file id.
func (c *Client) UploadComplete(ctx context.Context, sessionID string, etags []string) (string, error) {
	payload, err := c.request(ctx, "POST", "/media/v1/upload/"+Seg(sessionID)+"/complete", pyjson.O("eTags", etags))
	if err != nil {
		return "", err
	}
	fileID := dataOf(payload).Get("fileId")
	if !pyjson.Truthy(fileID) {
		return "", &Error{Msg: "upload completion did not return a fileId"}
	}
	return pyjson.Str(fileID), nil
}
