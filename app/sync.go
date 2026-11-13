package app

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/slidebolt/plugin-plex/internal/plex"
	domain "github.com/slidebolt/sb-domain"
)

func (a *App) syncLibraries(ctx context.Context, s *plexServer, now time.Time) {
	var result plex.Container[plex.Sections]
	if err := s.Client.Get(ctx, "/library/sections", nil, &result); err != nil {
		return
	}
	for _, section := range result.MediaContainer.Directory {
		s.Libraries[section.Key] = section
		_ = a.store.Save(domain.Entity{ID: "library-" + safeID(section.Key), Plugin: PluginID, DeviceID: s.ID, Type: "media_library", Name: section.Title, State: domain.MediaLibrary{ServerID: s.ID, Kind: section.Type, CanBrowse: true, CanSearch: true, UpdatedAt: &now}})
	}
}

func (a *App) syncClients(ctx context.Context, s *plexServer) {
	var result plex.Container[plex.Clients]
	if err := s.Client.Get(ctx, "/clients", nil, &result); err != nil {
		return
	}
	for _, client := range result.MediaContainer.Server {
		id := "player-" + safeID(client.MachineIdentifier)
		if id == "player-" {
			continue
		}
		_ = a.store.Save(domain.Device{ID: id, Plugin: PluginID, Name: client.Name})
		_ = a.store.Save(domain.Entity{ID: "player", Plugin: PluginID, DeviceID: id, Type: "media_player", Name: client.Name, Commands: []string{"media_play", "media_pause", "media_stop", "media_play_item", "media_seek"}, State: domain.MediaPlayer{State: "idle", ServerID: s.ID, Available: true}})
	}
}

func (a *App) syncSessions(ctx context.Context, s *plexServer) {
	var result plex.Container[plex.Sessions]
	if err := s.Client.Get(ctx, "/status/sessions", nil, &result); err != nil {
		return
	}
	for _, session := range result.MediaContainer.Metadata {
		id := session.Session.ID
		if id == "" {
			id = session.Player.MachineIdentifier + "-" + session.RatingKey
		}
		id = "session-" + safeID(id)
		ref := mediaRef(s.ID, "", session.LibraryMetadata)
		_ = a.store.Save(domain.Entity{ID: id, Plugin: PluginID, DeviceID: s.ID, Type: "media_session", Name: session.Title, State: domain.MediaSession{ServerID: s.ID, PlayerID: session.Player.MachineIdentifier, User: session.User.Title, Item: &ref, Title: session.Title, State: session.Player.State, PositionMS: session.ViewOffset, DurationMS: session.Duration}})
	}
}

func (a *App) syncDVR(ctx context.Context, s *plexServer, now time.Time) {
	var dvrs plex.Container[plex.DVRs]
	if err := s.Client.Get(ctx, "/livetv/dvrs", nil, &dvrs); err != nil || len(dvrs.MediaContainer.Dvr) == 0 {
		return
	}
	var providers plex.Container[plex.Providers]
	if err := s.Client.Get(ctx, "/media/providers", nil, &providers); err == nil {
		for _, p := range providers.MediaContainer.MediaProvider {
			if p.EPGSource != "" || strings.Contains(p.Identifier, "epg") {
				s.ProviderPath = providerPath(p)
				break
			}
		}
	}
	if s.ProviderPath == "" {
		return
	}
	var channels plex.Container[plex.Channels]
	if err := s.Client.Get(ctx, s.ProviderPath+"/lineups/dvr/channels", nil, &channels); err != nil {
		return
	}
	for _, ch := range channels.MediaContainer.Channel {
		id := ch.GridKey
		if id == "" {
			id = ch.ID
		}
		if id == "" {
			id = ch.VCN
		}
		s.Channels[id] = ch
	}
	if now.Sub(s.GuideAt) > 30*time.Minute {
		a.loadGuide(ctx, s, now)
	}
	for id, ch := range s.Channels {
		state := channelState(ch, s.Programs[id], now, s.GuideAt)
		_ = a.store.Save(domain.Entity{ID: "channel-" + safeID(ch.VCN), Plugin: PluginID, DeviceID: s.ID, Type: "tv_channel", Name: strings.TrimSpace(ch.VCN + " " + ch.Title), Commands: []string{"tv_record_current", "tv_record_airing", "tv_record_series"}, State: state})
	}
	a.syncSubscriptions(ctx, s)
}

func providerPath(p plex.Provider) string {
	if strings.HasPrefix(p.Identifier, "/") {
		return strings.TrimRight(p.Identifier, "/")
	}
	if strings.HasPrefix(p.Identifier, "tv.plex.providers") {
		return "/" + strings.TrimRight(p.Identifier, "/")
	}
	return "/tv.plex.providers.epg.cloud:" + strconv.Itoa(p.ID)
}

func (a *App) loadGuide(ctx context.Context, s *plexServer, now time.Time) {
	programs := map[string][]domain.TVProgram{}
	airings := map[string]airingRecord{}
	for id, ch := range s.Channels {
		for day := 0; day < 2; day++ {
			q := url.Values{"channelGridKey": {id}, "date": {now.AddDate(0, 0, day).Format("2006-01-02")}}
			var grid plex.Container[plex.MetadataPage]
			if err := s.Client.Get(ctx, s.ProviderPath+"/grid", q, &grid); err != nil {
				continue
			}
			for _, metadata := range grid.MediaContainer.Metadata {
				for _, airing := range metadata.Media {
					p := tvProgram(s.ID, metadata, airing)
					if p.AiringID == "" {
						continue
					}
					programs[id] = append(programs[id], p)
					airings[p.AiringID] = airingRecord{ChannelID: id, Program: p, Metadata: metadata}
				}
			}
		}
		_ = ch
	}
	s.Programs, s.Airings, s.GuideAt = programs, airings, now
}

func tvProgram(serverID string, m plex.LibraryMetadata, airing plex.Airing) domain.TVProgram {
	id := strings.Trim(string(airing.ID), `"`)
	series := m.GrandparentGUID
	if series == "" {
		series = m.GUID
	}
	return domain.TVProgram{AiringID: id, SeriesID: series, Title: firstNonempty(m.GrandparentTitle, m.Title), EpisodeTitle: func() string {
		if m.GrandparentTitle != "" {
			return m.Title
		}
		return ""
	}(), Season: m.ParentIndex, Episode: m.Index, Synopsis: m.Summary, StartsAt: time.Unix(airing.BeginsAt, 0), EndsAt: time.Unix(airing.EndsAt, 0), ProviderRef: func() *domain.MediaItemRef { r := mediaRef(serverID, "", m); return &r }()}
}

func channelState(ch plex.Channel, programs []domain.TVProgram, now, updated time.Time) domain.TVChannel {
	state := domain.TVChannel{Number: ch.VCN, Name: firstNonempty(ch.Title, ch.CallSign), Available: true, HD: string(ch.IsHD) == "1" || string(ch.IsHD) == "true"}
	if !updated.IsZero() {
		state.GuideUpdatedAt = &updated
	}
	for i := range programs {
		p := programs[i]
		if !now.Before(p.StartsAt) && now.Before(p.EndsAt) {
			copy := p
			state.CurrentProgram = &copy
		} else if p.StartsAt.After(now) && (state.NextProgram == nil || p.StartsAt.Before(state.NextProgram.StartsAt)) {
			copy := p
			state.NextProgram = &copy
		}
	}
	return state
}

func mediaRef(serverID, library string, m plex.LibraryMetadata) domain.MediaItemRef {
	id := m.RatingKey
	if id == "" {
		id = strings.TrimPrefix(m.Key, "/library/metadata/")
	}
	return domain.MediaItemRef{Provider: PluginID, ServerID: serverID, LibraryID: library, ItemID: plex.EncodeID(id)}
}

func (a *App) syncSubscriptions(ctx context.Context, s *plexServer) {
	var subscriptions plex.Container[plex.Subscriptions]
	if err := s.Client.Get(ctx, "/media/subscriptions", url.Values{"includeGrabs": {"1"}}, &subscriptions); err != nil {
		return
	}
	for _, sub := range subscriptions.MediaContainer.MediaSubscription {
		id := "rule-" + safeID(sub.Key)
		_ = a.store.Save(domain.Entity{ID: id, Plugin: PluginID, DeviceID: s.ID, Type: "tv_recording_rule", Name: sub.Title, Commands: []string{"tv_record_cancel"}, State: domain.TVRecordingRule{Enabled: true, Title: sub.Title, Provider: PluginID, ExternalID: sub.Key}})
	}
	var scheduled plex.Container[plex.Grabs]
	if err := s.Client.Get(ctx, "/media/subscriptions/scheduled", nil, &scheduled); err != nil {
		return
	}
	for _, grab := range scheduled.MediaContainer.MediaGrabOperation {
		m := grab.Metadata
		if len(m.Media) == 0 {
			continue
		}
		p := tvProgram(s.ID, m, m.Media[0])
		ch := domain.TVChannel{Number: m.Media[0].ChannelVCN, Name: m.Media[0].ChannelTitle, Available: true}
		_ = a.store.Save(domain.Entity{ID: "recording-" + safeID(p.AiringID), Plugin: PluginID, DeviceID: s.ID, Type: "tv_recording", Name: p.Title, Commands: []string{"tv_record_cancel"}, State: domain.TVRecording{Status: grab.Status, Provider: PluginID, ExternalID: strconv.Itoa(grab.MediaSubscriptionID), Channel: ch, Program: p, StartsAt: p.StartsAt, EndsAt: p.EndsAt}})
	}
}
