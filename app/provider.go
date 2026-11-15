package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/slidebolt/plugin-plex/internal/plex"
	domain "github.com/slidebolt/sb-domain"
	media "github.com/slidebolt/sb-media-sdk"
)

func (a *App) server(id string) *plexServer { a.mu.RLock(); defer a.mu.RUnlock(); return a.servers[id] }

func (a *App) Browse(req media.CatalogRequest) media.CatalogPage {
	s := a.server(req.ServerID)
	if s == nil {
		return media.CatalogPage{Error: "media server not found"}
	}
	path, err := catalogPath(req)
	if err != nil {
		return media.CatalogPage{Error: err.Error()}
	}
	limit := req.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	offset, _ := strconv.Atoi(req.Cursor)
	q := url.Values{"X-Plex-Container-Start": {strconv.Itoa(offset)}, "X-Plex-Container-Size": {strconv.Itoa(limit)}}
	if req.Query != "" {
		q.Set("title", req.Query)
	}
	if itemType := plexItemType(req.Kind); itemType != "" {
		q.Set("type", itemType)
	}
	var result plex.Container[plex.MetadataPage]
	ctx, cancel := context.WithTimeout(a.ctx, 15*time.Second)
	defer cancel()
	if err := s.Client.Get(ctx, path, q, &result); err != nil {
		return media.CatalogPage{Error: err.Error()}
	}
	items := make([]media.MediaItem, 0, len(result.MediaContainer.Metadata))
	for _, m := range result.MediaContainer.Metadata {
		items = append(items, mediaItem(s.ID, req.LibraryID, m))
	}
	total := result.MediaContainer.TotalSize
	if total == 0 {
		total = result.MediaContainer.Size
	}
	next := ""
	if offset+len(items) < total {
		next = strconv.Itoa(offset + len(items))
	}
	return media.CatalogPage{Items: items, NextCursor: next, Total: total}
}

func plexItemType(kind string) string {
	switch strings.ToLower(kind) {
	case "movie":
		return "1"
	case "show", "series":
		return "2"
	case "season":
		return "3"
	case "episode":
		return "4"
	case "artist":
		return "8"
	case "album":
		return "9"
	case "track", "audio":
		return "10"
	case "photo", "image":
		return "12"
	default:
		return ""
	}
}

func catalogPath(req media.CatalogRequest) (string, error) {
	if req.ItemID != "" {
		decoded, err := plex.DecodeID(req.ItemID)
		if err != nil {
			return "", fmt.Errorf("invalid item ID")
		}
		return "/library/metadata/" + url.PathEscape(decoded), nil
	}
	if req.ParentID != "" {
		decoded, err := plex.DecodeID(req.ParentID)
		if err != nil {
			return "", fmt.Errorf("invalid parent ID")
		}
		path := "/library/metadata/" + url.PathEscape(decoded) + "/children"
		if strings.EqualFold(req.Kind, "episode") {
			path = "/library/metadata/" + url.PathEscape(decoded) + "/allLeaves"
		}
		return path, nil
	}
	return "/library/sections/" + url.PathEscape(req.LibraryID) + "/all", nil
}

func mediaItem(serverID, libraryID string, m plex.LibraryMetadata) media.MediaItem {
	itemID := m.RatingKey
	if itemID == "" {
		itemID = strings.TrimPrefix(m.Key, "/library/metadata/")
	}
	ref := domain.MediaItemRef{Provider: PluginID, ServerID: serverID, LibraryID: libraryID, ItemID: plex.EncodeID(itemID)}
	var parent *domain.MediaItemRef
	if m.ParentRatingKey != "" {
		p := domain.MediaItemRef{Provider: PluginID, ServerID: serverID, LibraryID: libraryID, ItemID: plex.EncodeID(m.ParentRatingKey)}
		parent = &p
	}
	metadata := map[string]json.RawMessage{}
	putMetadata(metadata, "sortTitle", m.SortTitle)
	putMetadata(metadata, "description", m.Summary)
	putMetadata(metadata, "year", m.Year)
	putMetadata(metadata, "parentTitle", firstNonempty(m.ParentTitle, m.GrandparentTitle))
	putMetadata(metadata, "season", m.ParentIndex)
	putMetadata(metadata, "episode", m.Index)
	putMetadata(metadata, "availableAt", m.OriginallyAvailableAt)
	assets := []domain.MediaAsset{}
	if m.Thumb != "" {
		assets = append(assets, domain.MediaAsset{Kind: "thumbnail", Ref: "thumb"})
	}
	if m.Art != "" {
		assets = append(assets, domain.MediaAsset{Kind: "art", Ref: "art"})
	}
	return media.MediaItem{Ref: ref, Kind: m.Type, Title: m.Title, DurationMS: m.Duration, Parent: parent, Playable: m.Type != "show" && m.Type != "season" && m.Type != "artist" && m.Type != "album", Metadata: metadata, Assets: assets}
}

func putMetadata(metadata map[string]json.RawMessage, key string, value any) {
	switch v := value.(type) {
	case string:
		if v == "" {
			return
		}
	case int:
		if v == 0 {
			return
		}
	}
	b, _ := json.Marshal(value)
	metadata[key] = b
}

func (a *App) Artwork(req media.ArtworkRequest) media.ArtworkReply {
	s := a.server(req.ServerID)
	if s == nil {
		return media.ArtworkReply{Error: "media server not found"}
	}
	id, err := plex.DecodeID(req.ItemID)
	if err != nil {
		return media.ArtworkReply{Error: "invalid item ID"}
	}
	var result plex.Container[plex.MetadataPage]
	ctx, cancel := context.WithTimeout(a.ctx, 15*time.Second)
	defer cancel()
	if err := s.Client.Get(ctx, "/library/metadata/"+url.PathEscape(id), nil, &result); err != nil || len(result.MediaContainer.Metadata) == 0 {
		if err == nil {
			err = fmt.Errorf("media item not found")
		}
		return media.ArtworkReply{Error: err.Error()}
	}
	m := result.MediaContainer.Metadata[0]
	path := m.Thumb
	if req.Kind == "art" {
		path = m.Art
	}
	if path == "" {
		return media.ArtworkReply{Error: "artwork not available"}
	}
	b, contentType, err := s.Client.Bytes(ctx, path)
	if err != nil {
		return media.ArtworkReply{Error: err.Error()}
	}
	return media.ArtworkReply{Data: b, ContentType: contentType}
}

func (a *App) Resolve(req media.ResolveRequest) media.ResolveReply {
	if req.Source.Item == nil || req.Source.Item.Provider != PluginID {
		return media.ResolveReply{Error: "Plex resolves its own media items only"}
	}
	return media.ResolveReply{Error: "Plex playback is controlled through its advertised player, not a public source URL"}
}

func (a *App) Guide(req media.GuideRequest) media.GuidePage {
	s := a.server(req.ServerID)
	if s == nil {
		return media.GuidePage{Error: "media server not found"}
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	channels := make([]media.GuideChannel, 0, len(s.Channels))
	programs := []domain.TVProgram{}
	for id, ch := range s.Channels {
		if req.Channel != "" && req.Channel != ch.VCN && req.Channel != id {
			continue
		}
		state := channelState(ch, s.Programs[id], time.Now(), s.GuideAt)
		channels = append(channels, media.GuideChannel{ID: id, State: state})
		for _, p := range s.Programs[id] {
			if (!req.Start.IsZero() && !p.EndsAt.After(req.Start)) || (!req.End.IsZero() && !p.StartsAt.Before(req.End)) || (req.Query != "" && !strings.Contains(strings.ToLower(p.Title+" "+p.EpisodeTitle), strings.ToLower(req.Query))) {
				continue
			}
			programs = append(programs, p)
		}
	}
	sort.Slice(channels, func(i, j int) bool { return channels[i].State.Number < channels[j].State.Number })
	sort.Slice(programs, func(i, j int) bool { return programs[i].StartsAt.Before(programs[j].StartsAt) })
	offset, _ := strconv.Atoi(req.Cursor)
	limit := req.Limit
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	if offset > len(programs) {
		offset = len(programs)
	}
	end := offset + limit
	if end > len(programs) {
		end = len(programs)
	}
	next := ""
	if end < len(programs) {
		next = strconv.Itoa(end)
	}
	return media.GuidePage{Channels: channels, Programs: programs[offset:end], NextCursor: next}
}

func firstNonempty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
