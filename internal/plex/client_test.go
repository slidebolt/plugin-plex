package plex

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestResourcesSendsPlexIdentityAndKeepsTokenOutOfURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("X-Plex-Token") != "" {
			t.Fatal("token leaked into URL")
		}
		if got := r.Header.Get("X-Plex-Token"); got != "secret" {
			t.Fatalf("token header = %q", got)
		}
		if got := r.Header.Get("X-Plex-Client-Identifier"); got != "client" {
			t.Fatalf("client header = %q", got)
		}
		_ = json.NewEncoder(w).Encode([]Resource{{Name: "Server", Product: "Plex Media Server", ClientIdentifier: "server-id"}})
	}))
	defer server.Close()
	resources, err := New(server.URL, "secret", "client").Resources(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(resources) != 1 || resources[0].ClientIdentifier != "server-id" {
		t.Fatalf("resources = %#v", resources)
	}
}

func TestPlayerCommandUsesTargetHeaderAndKeepsTokenOutOfURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/player/playback/playMedia" || r.URL.Query().Get("key") != "/library/metadata/42" {
			t.Fatalf("request = %s", r.URL.String())
		}
		if r.URL.Query().Get("X-Plex-Token") != "" {
			t.Fatal("token leaked into URL")
		}
		if r.Header.Get("X-Plex-Token") != "secret" || r.Header.Get("X-Plex-Target-Client-Identifier") != "player-id" {
			t.Fatalf("headers = %#v", r.Header)
		}
	}))
	defer server.Close()
	err := New(server.URL, "secret", "controller").PlayerCommand(context.Background(), "player-id", "/player/playback/playMedia", url.Values{"key": {"/library/metadata/42"}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestBytesReturnsContentType(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("image"))
	}))
	defer server.Close()
	b, contentType, err := New(server.URL, "", "client").Bytes(context.Background(), "/thumb")
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "image" || contentType != "image/jpeg" {
		t.Fatalf("got %q %q", b, contentType)
	}
}

func TestEncodedIDsRoundTrip(t *testing.T) {
	original := "1234/path"
	encoded := EncodeID(original)
	decoded, err := DecodeID(encoded)
	if err != nil || decoded != original {
		t.Fatalf("round trip = %q, %v", decoded, err)
	}
}

func TestLibraryMetadataSeparatesCanonicalAndExternalGUIDs(t *testing.T) {
	var metadata LibraryMetadata
	payload := []byte(`{"ratingKey":"42","guid":"plex://show/example","Guid":[{"id":"external://example"}],"title":"Example Show"}`)
	if err := json.Unmarshal(payload, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.GUID != "plex://show/example" || metadata.RatingKey != "42" || metadata.Title != "Example Show" {
		t.Fatalf("metadata = %#v", metadata)
	}
}

func TestDVRDistinguishesLineupStringFromLineupArray(t *testing.T) {
	var result Container[DVRs]
	raw := []byte(`{"MediaContainer":{"Dvr":[{"key":"2","lineup":"lineup://example","Lineup":[{"key":"enabled"}]}]}}`)
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if got := result.MediaContainer.Dvr[0]; got.Lineup != "lineup://example" || len(got.Lineups) != 1 {
		t.Fatalf("DVR = %#v", got)
	}
}
