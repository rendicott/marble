package main

// Set at link time by release builds, e.g.:
//
//	go build -ldflags "-X main.version=v0.1.0 -X main.commit=abc123 -X main.date=2026-07-20T00:00:00Z"
var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

func versionString() string {
	return version + " (commit " + commit + ", built " + date + ")"
}
