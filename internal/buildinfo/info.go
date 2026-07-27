package buildinfo

import (
	"runtime"
	"time"
)

const AdminAPIVersion = "1"

type Provider struct {
	version   string
	revision  string
	builtAt   string
	imageRef  string
	startedAt time.Time
}

type Info struct {
	Version             string    `json:"version"`
	Revision            string    `json:"revision"`
	BuiltAt             string    `json:"builtAt"`
	GoVersion           string    `json:"goVersion"`
	Platform            string    `json:"platform"`
	ImageRef            string    `json:"imageRef,omitempty"`
	StartedAt           time.Time `json:"startedAt"`
	UptimeSeconds       int64     `json:"uptimeSeconds"`
	AdminAPIVersion     string    `json:"adminApiVersion"`
	ConfigSchemaVersion int       `json:"configSchemaVersion"`
}

func New(version, revision, builtAt, imageRef string, startedAt time.Time) Provider {
	return Provider{version: fallback(version), revision: fallback(revision), builtAt: fallback(builtAt), imageRef: imageRef, startedAt: startedAt}
}

func (p Provider) Snapshot(configSchemaVersion int) Info {
	uptime := time.Since(p.startedAt)
	if uptime < 0 {
		uptime = 0
	}
	return Info{
		Version: p.version, Revision: p.revision, BuiltAt: p.builtAt,
		GoVersion: runtime.Version(), Platform: runtime.GOOS + "/" + runtime.GOARCH,
		ImageRef: p.imageRef, StartedAt: p.startedAt, UptimeSeconds: int64(uptime.Seconds()),
		AdminAPIVersion: AdminAPIVersion, ConfigSchemaVersion: configSchemaVersion,
	}
}

func fallback(value string) string {
	if value == "" {
		return "unknown"
	}
	return value
}
