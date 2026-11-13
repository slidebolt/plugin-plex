# Git Workflow for plugin-plex

Layer 7 plugin. It depends on `sb-domain`, `sb-media-sdk`,
`sb-messenger-sdk`, `sb-storage-sdk`, `sb-runtime`, and `sb-contract`.

Validate with `go test ./...` and `go build ./cmd/plugin-plex`. Release tags
publish linux amd64 and arm64 binaries.
