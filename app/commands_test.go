package app

import (
	"testing"

	"github.com/slidebolt/plugin-plex/internal/plex"
	domain "github.com/slidebolt/sb-domain"
)

func TestPlayMediaQueryUsesOpaqueItemReference(t *testing.T) {
	s := &plexServer{ID: "server", Resource: plex.Resource{ClientIdentifier: "server-machine"}, Client: plex.New("https://media.example:32400", "secret", "controller"), Libraries: map[string]plex.Section{"1": {Type: "movie"}}}
	ref := domain.MediaItemRef{Provider: PluginID, ServerID: "server", LibraryID: "1", ItemID: plex.EncodeID("42")}
	query, err := playMediaQuery(s, ref, 1200)
	if err != nil {
		t.Fatal(err)
	}
	if query.Get("key") != "/library/metadata/42" || query.Get("offset") != "1200" || query.Get("machineIdentifier") != "server-machine" || query.Get("type") != "video" {
		t.Fatalf("query = %v", query)
	}
	if query.Get("X-Plex-Token") != "" {
		t.Fatal("token leaked into playback query")
	}
}
