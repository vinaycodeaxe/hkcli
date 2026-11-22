// Package store manages the local store: one directory per Hike account holding
// session.json (the login), device.json, hike.db, media/ and lock files.
package store

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/vinaycodeaxe/hkcli/internal/pyjson"
	"github.com/vinaycodeaxe/hkcli/internal/uuid"
)

// Store is one store directory.
type Store struct {
	Root string
}

// New opens the store at root, or the default store when root is empty.
func New(root string) *Store {
	if root == "" {
		return &Store{Root: DefaultRoot()}
	}
	return &Store{Root: filepath.Clean(ExpandUser(root))}
}

// DefaultRoot is $HKCLI_STORE, else ~/.hkcli. A store made by the Python
// hikecli (~/.hikecli) is used when ~/.hkcli does not exist yet.
func DefaultRoot() string {
	if env := os.Getenv("HKCLI_STORE"); env != "" {
		return filepath.Clean(ExpandUser(env))
	}
	home := homeDir()
	root := filepath.Join(home, ".hkcli")
	if _, err := os.Stat(root); errors.Is(err, fs.ErrNotExist) {
		legacy := filepath.Join(home, ".hikecli")
		if info, err := os.Stat(legacy); err == nil && info.IsDir() {
			return legacy
		}
	}
	return root
}

func homeDir() string {
	if home, err := os.UserHomeDir(); err == nil {
		return home
	}
	return "."
}

// ExpandUser replaces a leading ~ with the home directory.
func ExpandUser(path string) string {
	if path == "~" {
		return homeDir()
	}
	if strings.HasPrefix(path, "~/") || (runtime.GOOS == "windows" && strings.HasPrefix(path, `~\`)) {
		return filepath.Join(homeDir(), path[2:])
	}
	return path
}

func (s *Store) path(name string) string { return filepath.Join(s.Root, name) }

// DBPath is the SQLite database.
func (s *Store) DBPath() string { return s.path("hike.db") }

// SessionPath holds the login tokens.
func (s *Store) SessionPath() string { return s.path("session.json") }

// DevicePath holds this store's web device id.
func (s *Store) DevicePath() string { return s.path("device.json") }

// MediaDir is where downloads go by default.
func (s *Store) MediaDir() string { return s.path("media") }

// LockPath is held while a login or sync runs.
func (s *Store) LockPath() string { return s.path("LOCK") }

// RefreshLockPath serializes token refreshes across processes.
func (s *Store) RefreshLockPath() string { return s.path("refresh.lock") }

// QRPNGPath is the QR code image saved during login.
func (s *Store) QRPNGPath() string { return s.path("login-qr.png") }

// Ensure creates the store directory (private to the user on POSIX).
func (s *Store) Ensure() error {
	if err := os.MkdirAll(s.Root, 0o700); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		_ = os.Chmod(s.Root, 0o700)
	}
	return nil
}

// NowMS is the current time in epoch milliseconds.
func NowMS() int64 { return time.Now().UnixMilli() }

// writeJSONAtomic writes data through a temp file and a rename, so readers never
// see a half-written file.
func writeJSONAtomic(path string, data string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, err = f.WriteString(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if runtime.GOOS != "windows" {
		_ = os.Chmod(tmp, 0o600)
	}
	// Windows refuses to replace a file another process has open; readers only hold it briefly.
	for attempt := 0; ; attempt++ {
		err = os.Rename(tmp, path)
		if err == nil || attempt == 19 || !isSharingError(err) {
			return err
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Session is the saved login. Its JSON field names match the Python version.
type Session struct {
	UserID       string
	AccessToken  string
	RefreshToken string
	Profile      pyjson.Object
	LoginMethod  string
	CreatedAt    int64
	RefreshedAt  int64
	Revoked      string // why the login stopped working; "" while it works
}

// DisplayName is the profile name, else the phone number, else the user id.
func (s *Session) DisplayName() string {
	return pyjson.Str(pyjson.Or(pyjson.Or(s.Profile.Get("name"), s.Profile.Get("phone")), s.UserID))
}

func (s *Session) json() string {
	var revoked any
	if s.Revoked != "" {
		revoked = s.Revoked
	}
	profile := s.Profile
	if profile == nil {
		profile = pyjson.Object{}
	}
	return pyjson.DumpsIndent(pyjson.O(
		"user_id", s.UserID,
		"access_token", s.AccessToken,
		"refresh_token", s.RefreshToken,
		"profile", profile,
		"login_method", s.LoginMethod,
		"created_at", s.CreatedAt,
		"refreshed_at", s.RefreshedAt,
		"revoked", revoked,
	))
}

func readJSONObject(path string) pyjson.Object {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	v, err := pyjson.Loads(data)
	if err != nil {
		return nil
	}
	obj, _ := v.(pyjson.Object)
	return obj
}

// LoadSession returns the saved login, or nil when there is none.
func (s *Store) LoadSession() *Session {
	raw := readJSONObject(s.SessionPath())
	if raw == nil || !pyjson.Truthy(raw.Get("user_id")) || !pyjson.Truthy(raw.Get("access_token")) {
		return nil
	}
	sess := &Session{
		UserID:       pyjson.Str(raw.Get("user_id")),
		AccessToken:  pyjson.Str(raw.Get("access_token")),
		RefreshToken: optString(raw, "refresh_token"),
		Profile:      raw.Obj("profile"),
		LoginMethod:  "qr",
		CreatedAt:    optInt(raw, "created_at"),
		RefreshedAt:  optInt(raw, "refreshed_at"),
		Revoked:      optString(raw, "revoked"),
	}
	if _, ok := raw.Lookup("login_method"); ok {
		sess.LoginMethod = optString(raw, "login_method")
	}
	return sess
}

func optString(o pyjson.Object, key string) string {
	if v := o.Get(key); v != nil {
		return pyjson.Str(v)
	}
	return ""
}

func optInt(o pyjson.Object, key string) int64 {
	switch v := o.Get(key).(type) {
	case int64:
		return v
	case float64:
		return int64(v)
	}
	return 0
}

// SaveSession writes the login atomically.
func (s *Store) SaveSession(sess *Session) error {
	if err := s.Ensure(); err != nil {
		return err
	}
	return writeJSONAtomic(s.SessionPath(), sess.json())
}

// DeleteSession forgets the login; it reports whether one was saved.
func (s *Store) DeleteSession() (bool, error) {
	err := os.Remove(s.SessionPath())
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// DeviceID is the stable per-store web device id (Hike Web keeps it in
// localStorage as hike_device_id).
func (s *Store) DeviceID() (string, error) {
	if raw := readJSONObject(s.DevicePath()); pyjson.Truthy(raw.Get("deviceId")) {
		return pyjson.Str(raw.Get("deviceId")), nil
	}
	id := uuid.New()
	if err := s.Ensure(); err != nil {
		return "", err
	}
	return id, writeJSONAtomic(s.DevicePath(), pyjson.DumpsIndent(pyjson.O("deviceId", id)))
}
