package app

import (
	"context"
	"encoding/json"
	"log"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/slidebolt/plugin-plex/internal/plex"
	domain "github.com/slidebolt/sb-domain"
	storage "github.com/slidebolt/sb-storage-sdk"
)

func (a *App) syncLibraries(ctx context.Context, s *plexServer, now time.Time) {
	var result plex.Container[plex.Sections]
	if err := s.Client.Get(ctx, "/library/sections", nil, &result); err != nil {
		return
	}
	for _, section := range result.MediaContainer.Directory {
		s.Libraries[section.Key] = section
		_ = a.store.Save(domain.Entity{ID: "library-" + safeID(section.Key), Plugin: PluginID, DeviceID: s.ID, Type: "media_library", Name: section.Title, State: domain.MediaLibrary{ServerID: s.ID, LibraryID: section.Key, Kind: section.Type, CanBrowse: true, CanSearch: true, UpdatedAt: &now}})
	}
}

func (a *App) syncClients(ctx context.Context, s *plexServer) {
	var result plex.Container[plex.Clients]
	if err := s.Client.Get(ctx, "/clients", nil, &result); err != nil {
		return
	}
	clients := make(map[string]plex.PlexClient, len(result.MediaContainer.Server))
	for _, client := range result.MediaContainer.Server {
		id := "player-" + safeID(client.MachineIdentifier)
		if id == "player-" {
			continue
		}
		clients[id] = client
		_ = a.store.Save(domain.Device{ID: id, Plugin: PluginID, Class: "media_player", Name: client.Name, SecurityGroups: []string{domain.SecurityGroupMedia}})
		_ = a.store.Save(domain.Entity{ID: "player", Plugin: PluginID, DeviceID: id, Type: "media_player", Name: client.Name, Commands: []string{"media_play", "media_pause", "media_stop", "media_seek"}, State: domain.MediaPlayer{State: "idle", ServerID: s.ID, Available: true}})
	}
	a.mu.Lock()
	s.Clients = clients
	a.mu.Unlock()
}

func (a *App) syncSessions(ctx context.Context, s *plexServer) {
	var result plex.Container[plex.Sessions]
	if err := s.Client.Get(ctx, "/status/sessions", nil, &result); err != nil {
		return
	}
	items := make([]storage.Keyed, 0, len(result.MediaContainer.Metadata))
	for _, session := range result.MediaContainer.Metadata {
		id := session.Session.ID
		if id == "" {
			id = session.Player.MachineIdentifier + "-" + session.RatingKey
		}
		id = "session-" + safeID(id)
		ref := mediaRef(s.ID, "", session.LibraryMetadata)
		playerID := "player-" + safeID(session.Player.MachineIdentifier)
		playerRef := ""
		if playerID != "player-" {
			playerRef = PluginID + "." + playerID + ".player"
		}
		items = append(items, a.healthyEntity(domain.Entity{ID: id, Plugin: PluginID, DeviceID: s.ID, Type: "media_session", Name: session.Title, State: domain.MediaSession{ServerID: s.ID, PlayerID: playerRef, User: session.User.Title, Item: &ref, Title: session.Title, State: session.Player.State, PositionMS: session.ViewOffset, DurationMS: session.Duration}}))
	}
	if _, err := storage.ReconcileSnapshot(a.store, storage.Snapshot{Pattern: PluginID + "." + s.ID + ".session-*", Items: items, OnMissing: storage.MissingDelete}); err != nil {
		log.Printf("plugin-plex: reconcile sessions for %s: %v", s.ID, err)
	}
}

func (a *App) syncDVR(ctx context.Context, s *plexServer, now time.Time) {
	var dvrs plex.Container[plex.DVRs]
	if err := s.Client.Get(ctx, "/livetv/dvrs", nil, &dvrs); err != nil {
		log.Printf("plugin-plex: sync DVRs for %s: %v", s.ID, err)
		return
	}
	if len(dvrs.MediaContainer.Dvr) == 0 {
		log.Printf("plugin-plex: server %s has no DVR", s.ID)
		return
	}
	_ = a.store.Save(domain.Entity{ID: "recorder", Plugin: PluginID, DeviceID: s.ID, Type: "recorder", Name: "Plex DVR", Commands: []string{"record_start", "record_cancel", "recording_rule_create", "recording_rule_delete"}, State: domain.Recorder{Status: "idle"}})
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
		log.Printf("plugin-plex: server %s DVR has no EPG provider", s.ID)
		return
	}
	var channels plex.Container[plex.Channels]
	if err := s.Client.Get(ctx, s.ProviderPath+"/lineups/dvr/channels", nil, &channels); err != nil {
		log.Printf("plugin-plex: sync DVR channels for %s: %v", s.ID, err)
		return
	}
	log.Printf("plugin-plex: discovered %d DVR channels on %s", len(channels.MediaContainer.Channel), s.ID)
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
		_ = a.store.Save(domain.Entity{ID: "channel-" + safeID(ch.VCN), Plugin: PluginID, DeviceID: s.ID, Type: "tv_channel", Name: strings.TrimSpace(ch.VCN + " " + ch.Title), State: state})
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
	log.Printf("plugin-plex: loaded %d guide airings on %s", len(airings), s.ID)
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
	rules := make([]storage.Keyed, 0, len(subscriptions.MediaContainer.MediaSubscription))
	for _, sub := range subscriptions.MediaContainer.MediaSubscription {
		id := "rule-" + safeID(sub.Key)
		external, _ := json.Marshal(sub.Key)
		name, query := subscriptionQuery(s.ID, sub)
		attributes := query.Attributes
		if attributes == nil {
			attributes = map[string]json.RawMessage{}
		}
		attributes["externalID"] = external
		if sub.AiringsType != "" {
			attributes["airingsType"] = mustJSON(sub.AiringsType)
		}
		query.Attributes = attributes
		selection := "one"
		if sub.Type == 2 {
			selection = "all"
		}
		rules = append(rules, a.healthyEntity(domain.Entity{ID: id, Plugin: PluginID, DeviceID: s.ID, Type: "recording_rule", Name: name, Commands: []string{"recording_rule_delete"}, State: domain.RecordingRule{Enabled: true, Query: query, Selection: selection, Recorder: PluginID + "." + s.ID + ".recorder"}}))
	}
	if _, err := storage.ReconcileSnapshot(a.store, storage.Snapshot{Pattern: PluginID + "." + s.ID + ".rule-*", Items: rules, OnMissing: storage.MissingDelete}); err != nil {
		log.Printf("plugin-plex: reconcile recording rules for %s: %v", s.ID, err)
		return
	}
	var scheduled plex.Container[plex.Grabs]
	if err := s.Client.Get(ctx, "/media/subscriptions/scheduled", nil, &scheduled); err != nil {
		return
	}
	schedules := make([]storage.Keyed, 0, len(scheduled.MediaContainer.MediaGrabOperation))
	for _, grab := range scheduled.MediaContainer.MediaGrabOperation {
		m := grab.Metadata
		if len(m.Media) == 0 {
			continue
		}
		p := tvProgram(s.ID, m, m.Media[0])
		input, _ := json.Marshal(domain.RecordStart{Source: domain.PlayableSource{Kind: "live", Entity: PluginID + "." + s.ID + ".channel-" + safeID(m.Media[0].ChannelVCN)}, Metadata: map[string]json.RawMessage{"program": mustJSON(p), "externalID": mustJSON(strconv.Itoa(grab.MediaSubscriptionID))}})
		schedule := domain.Schedule{Enabled: grab.Status != "canceled", Steps: []domain.ScheduleStep{{ID: "start", Trigger: domain.Trigger{Kind: "at", At: &p.StartsAt}, Actions: []domain.Action{{Target: PluginID + "." + s.ID + ".recorder", Command: "record_start", Input: input}}}, {ID: "stop", Trigger: domain.Trigger{Kind: "at", At: &p.EndsAt}, Actions: []domain.Action{{Target: PluginID + "." + s.ID + ".recorder", Command: "record_stop"}}}}}
		schedules = append(schedules, a.healthyEntity(domain.Entity{ID: "recording-" + safeID(p.AiringID), Plugin: PluginID, DeviceID: s.ID, Type: "schedule", Name: p.Title, State: schedule}))
	}
	if _, err := storage.ReconcileSnapshot(a.store, storage.Snapshot{Pattern: PluginID + "." + s.ID + ".recording-*", Items: schedules, OnMissing: storage.MissingDelete}); err != nil {
		log.Printf("plugin-plex: reconcile scheduled recordings for %s: %v", s.ID, err)
	}
}

func subscriptionQuery(serverID string, sub plex.Subscription) (string, domain.MediaQuery) {
	name := strings.TrimSpace(sub.Title)
	query := domain.MediaQuery{}
	if sub.Directory == nil {
		query.Title = name
		return name, query
	}
	if strings.TrimSpace(sub.Directory.Title) != "" {
		name = strings.TrimSpace(sub.Directory.Title)
	}
	query.Kind = sub.Directory.Type
	query.Title = name
	providerID := firstNonempty(sub.Directory.GUID, sub.Directory.RatingKey)
	if providerID != "" {
		query.Item = &domain.MediaItemRef{Provider: PluginID, ServerID: serverID, LibraryID: strconv.Itoa(sub.TargetLibrarySectionID), ItemID: plex.EncodeID(providerID)}
	}
	if sub.Directory.Thumb != "" {
		query.Attributes = map[string]json.RawMessage{"thumbnail": mustJSON(sub.Directory.Thumb)}
	}
	return name, query
}

func mustJSON(value any) json.RawMessage { data, _ := json.Marshal(value); return data }
