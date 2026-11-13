# SlideBolt Plex Plugin

Connects to Plex with the PIN authorization flow and exposes provider-neutral
media servers, libraries, catalog browsing, players, sessions, Live TV guide,
DVR rules, scheduled recordings, and completed recordings.

Plex owns Plex-initiated media files and library placement. SlideBolt mirrors
their lifecycle. Tokens are stored only in plugin-private storage and are never
returned in entities, events, logs, or artwork URLs.

## Configuration

- `PLEX_REFRESH_INTERVAL`: reconciliation interval (default `5m`).

Use the `media_provider_connect` command on
`plugin-plex.account.connection`, open the returned claim URL, and approve the
PIN with the Plex account that owns the DVR.
