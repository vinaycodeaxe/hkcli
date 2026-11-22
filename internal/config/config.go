// Package config holds the endpoints and client identity used by Hike Web
// (web.hikeapp.com).
//
// The values come from the config object in the Hike Web bundle (apiBase, qrWs,
// mqtt, appVersion, buildNumber). Each one can be overridden with an HKCLI_*
// environment variable, which is how the tests point hkcli at a fake server.
package config

import (
	"os"
	"runtime"
	"strings"

	"github.com/vinaycodeaxe/hkcli/internal/version"
)

const (
	AppID     = "hike-messenger"
	WebOrigin = "https://web.hikeapp.com"
)

// UserAgent identifies hkcli in HTTP and WebSocket requests.
func UserAgent() string { return "hkcli/" + version.Version }

func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

// APIBase is the REST API root without a trailing slash.
func APIBase() string {
	return strings.TrimRight(env("HKCLI_API_BASE", "https://api-ext.hikeapp.com"), "/")
}

// SessionWSBase is the QR login WebSocket root without a trailing slash.
func SessionWSBase() string {
	return strings.TrimRight(env("HKCLI_SESSION_WS", "wss://session.hikeapp.com"), "/")
}

// MQTTWSURL is the message relay (MQTT over WebSocket).
func MQTTWSURL() string { return env("HKCLI_MQTT_WS", "wss://relay-ws.hikeapp.com:443/mqtt") }

// AppVersion is the Hike Web version hkcli reports.
func AppVersion() string { return env("HKCLI_APP_VERSION", "1.3.2") }

// BuildNumber is the Hike Web build hkcli reports.
func BuildNumber() string { return env("HKCLI_BUILD_NUMBER", "39") }

// DeviceDescription is sent as deviceOsVersion during OTP login
// (Hike Web sends navigator.userAgent).
func DeviceDescription() string {
	return UserAgent() + " (" + runtime.GOOS + " " + runtime.GOARCH + ")"
}

// ReadOnlyFromEnv reports whether HKCLI_READONLY asks for read-only mode.
func ReadOnlyFromEnv() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("HKCLI_READONLY"))) {
	case "", "0", "false", "no":
		return false
	}
	return true
}
