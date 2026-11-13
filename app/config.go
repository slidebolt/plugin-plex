package app

import (
	"os"
	"time"
)

type config struct{ Refresh time.Duration }

func loadConfig() config {
	d := 5 * time.Minute
	if raw := os.Getenv("PLEX_REFRESH_INTERVAL"); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil && parsed >= time.Minute {
			d = parsed
		}
	}
	return config{Refresh: d}
}
