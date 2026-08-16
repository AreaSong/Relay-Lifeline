package buildinfo

import (
	"os"
	"runtime"
	"time"
)

const AdminAPIVersion = "2"

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
	Process             Process   `json:"process"`
}

type Process struct {
	PID               int       `json:"pid"`
	ParentPID         int       `json:"parentPid"`
	Goroutines        int       `json:"goroutines"`
	CPUCount          int       `json:"cpuCount"`
	GOMAXPROCS        int       `json:"gomaxprocs"`
	HeapAllocBytes    uint64    `json:"heapAllocBytes"`
	HeapInuseBytes    uint64    `json:"heapInuseBytes"`
	StackInuseBytes   uint64    `json:"stackInuseBytes"`
	SystemMemoryBytes uint64    `json:"systemMemoryBytes"`
	GCCycles          uint32    `json:"gcCycles"`
	SampledAt         time.Time `json:"sampledAt"`
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
		Process: processSnapshot(),
	}
}

func processSnapshot() Process {
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	return Process{
		PID: os.Getpid(), ParentPID: os.Getppid(), Goroutines: runtime.NumGoroutine(),
		CPUCount: runtime.NumCPU(), GOMAXPROCS: runtime.GOMAXPROCS(0),
		HeapAllocBytes: memory.HeapAlloc, HeapInuseBytes: memory.HeapInuse,
		StackInuseBytes: memory.StackInuse, SystemMemoryBytes: memory.Sys,
		GCCycles: memory.NumGC, SampledAt: time.Now(),
	}
}

func fallback(value string) string {
	if value == "" {
		return "unknown"
	}
	return value
}
