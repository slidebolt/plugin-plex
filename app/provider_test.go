package app

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/slidebolt/plugin-plex/internal/plex"
	domain "github.com/slidebolt/sb-domain"
	media "github.com/slidebolt/sb-media-sdk"
)

func TestTVProgramMapsAiringWithoutCredentials(t *testing.T) {
	now := time.Now().Unix()
	p := tvProgram("server", plex.LibraryMetadata{RatingKey: "42", Title: "Pilot", GrandparentTitle: "Example Show", ParentIndex: 1, Index: 2, GUID: "plex://episode/42", GrandparentGUID: "plex://show/7"}, plex.Airing{ID: json.RawMessage("123"), BeginsAt: now, EndsAt: now + 3600})
	if p.AiringID != "123" || p.Title != "Example Show" || p.EpisodeTitle != "Pilot" || p.Season != 1 || p.Episode != 2 {
		t.Fatalf("program = %#v", p)
	}
	if p.ProviderRef == nil || p.ProviderRef.Provider != PluginID || p.ProviderRef.ServerID != "server" {
		t.Fatalf("provider ref = %#v", p.ProviderRef)
	}
}

func TestCatalogPathTranslatesShowEpisodeBrowse(t *testing.T) {
	showID := plex.EncodeID("2543")
	path, err := catalogPath(media.CatalogRequest{LibraryID: "2", ParentID: showID, Kind: "episode"})
	if err != nil {
		t.Fatal(err)
	}
	if path != "/library/metadata/2543/allLeaves" {
		t.Fatalf("path = %q", path)
	}
}

func TestCatalogPathUsesDirectChildrenForGenericContainers(t *testing.T) {
	path, err := catalogPath(media.CatalogRequest{LibraryID: "2", ParentID: plex.EncodeID("42")})
	if err != nil {
		t.Fatal(err)
	}
	if path != "/library/metadata/42/children" {
		t.Fatalf("path = %q", path)
	}
}

func TestPlexItemTypeTranslatesGenericKinds(t *testing.T) {
	if plexItemType("show") != "2" || plexItemType("episode") != "4" || plexItemType("unknown") != "" {
		t.Fatal("generic media kinds were not translated to Plex type identifiers")
	}
}

func TestChannelStateSelectsCurrentAndNext(t *testing.T) {
	now := time.Now()
	current := tvProgram("s", plex.LibraryMetadata{Title: "Current"}, plex.Airing{ID: json.RawMessage("1"), BeginsAt: now.Add(-time.Minute).Unix(), EndsAt: now.Add(time.Minute).Unix()})
	next := tvProgram("s", plex.LibraryMetadata{Title: "Next"}, plex.Airing{ID: json.RawMessage("2"), BeginsAt: now.Add(time.Minute).Unix(), EndsAt: now.Add(2 * time.Minute).Unix()})
	state := channelState(plex.Channel{VCN: "7.1", Title: "Test"}, []domain.TVProgram{next, current}, now, now)
	if state.CurrentProgram == nil || state.CurrentProgram.Title != "Current" || state.NextProgram == nil || state.NextProgram.Title != "Next" {
		t.Fatalf("state = %#v", state)
	}
}

func TestAccountDeviceHasEmptyEntityArray(t *testing.T) {
	device := accountDevice()
	if device.Entities == nil {
		t.Fatal("account device entities must serialize as an empty array")
	}
}

func TestSubscriptionQueryUsesProviderTargetInsteadOfTemplateTitle(t *testing.T) {
	subscription := plex.Subscription{
		Key: "42", Type: 2, Title: "All Episodes", TargetLibrarySectionID: 7,
		Directory: &plex.SubscriptionDirectory{GUID: "plex://show/example", Title: "Example Show", Type: "show", Thumb: "https://media.example/thumb.jpg"},
	}
	name, query := subscriptionQuery("server", subscription)
	if name != "Example Show" || query.Title != "Example Show" || query.Kind != "show" {
		t.Fatalf("name/query = %q / %#v", name, query)
	}
	if query.Item == nil || query.Item.Provider != PluginID || query.Item.ServerID != "server" || query.Item.LibraryID != "7" {
		t.Fatalf("item = %#v", query.Item)
	}
	if len(query.Attributes["thumbnail"]) == 0 {
		t.Fatal("thumbnail was not preserved")
	}
}

func TestSubscriptionQueryFallsBackWhenProviderOmitsTarget(t *testing.T) {
	name, query := subscriptionQuery("server", plex.Subscription{Title: "Single Recording"})
	if name != "Single Recording" || query.Title != "Single Recording" || query.Item != nil {
		t.Fatalf("name/query = %q / %#v", name, query)
	}
}
