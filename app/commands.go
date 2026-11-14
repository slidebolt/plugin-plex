package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/slidebolt/plugin-plex/internal/plex"
	domain "github.com/slidebolt/sb-domain"
	messenger "github.com/slidebolt/sb-messenger-sdk"
)

func (a *App) handleCommand(addr messenger.Address, cmd any) {
	switch c := cmd.(type) {
	case domain.MediaProviderConnect:
		go a.connect()
	case domain.MediaProviderDisconnect:
		a.disconnect()
	case domain.RecordStart:
		var airingID string
		_ = json.Unmarshal(c.Metadata["airingID"], &airingID)
		if airingID != "" {
			a.recordAiring(addr.DeviceID, airingID, false, false)
		}
	case domain.RecordCancel:
		if c.Reference != "" {
			a.cancelExternal(addr.DeviceID, c.Reference)
		}
	case domain.RecordingRuleCreate:
		a.createRecordingRule(addr.DeviceID, c)
	case domain.RecordingRuleDelete:
		if c.Reference != "" {
			a.cancelExternal(addr.DeviceID, c.Reference)
		}
	case domain.MediaPlay, domain.MediaPause, domain.MediaStop, domain.MediaSeek:
		if err := a.controlPlayer(addr, cmd); err != nil {
			log.Printf("plugin-plex: control player %s: %v", addr.Key(), err)
		}
	}
}

var playerCommandID atomic.Uint64

func (a *App) controlPlayer(addr messenger.Address, command any) error {
	s, client := a.findPlayer(addr.DeviceID)
	if s == nil || client == nil {
		return fmt.Errorf("Plex player is not available")
	}
	query := url.Values{"commandID": {strconv.FormatUint(playerCommandID.Add(1), 10)}}
	path := ""
	state := "idle"
	var item *domain.MediaItemRef
	var position int64
	switch cmd := command.(type) {
	case domain.MediaPlay:
		path = "/player/playback/play"
		state = "playing"
		position = cmd.PositionMS
		if cmd.Source != nil && cmd.Source.Item != nil {
			playQuery, err := playMediaQuery(s, *cmd.Source.Item, cmd.PositionMS)
			if err != nil {
				return err
			}
			for key, values := range playQuery {
				query[key] = values
			}
			path = "/player/playback/playMedia"
			item = cmd.Source.Item
		}
	case domain.MediaPause:
		path, state = "/player/playback/pause", "paused"
	case domain.MediaStop:
		path, state = "/player/playback/stop", "idle"
	case domain.MediaSeek:
		path, state, position = "/player/playback/seekTo", "playing", cmd.PositionMS
		query.Set("offset", strconv.FormatInt(cmd.PositionMS, 10))
	default:
		return fmt.Errorf("unsupported player command %T", command)
	}
	ctx, cancel := context.WithTimeout(a.ctx, 15*time.Second)
	defer cancel()
	if err := s.Client.PlayerCommand(ctx, client.MachineIdentifier, path, query); err != nil {
		return err
	}
	return a.updatePlayerState(addr, state, position, item)
}

func playMediaQuery(s *plexServer, item domain.MediaItemRef, positionMS int64) (url.Values, error) {
	if item.Provider != PluginID || item.ServerID != s.ID {
		return nil, fmt.Errorf("Plex player requires an item from its Plex server")
	}
	id, err := plex.DecodeID(item.ItemID)
	if err != nil {
		return nil, fmt.Errorf("decode media item: %w", err)
	}
	serverURL, err := url.Parse(s.Client.BaseURL)
	if err != nil {
		return nil, err
	}
	port := serverURL.Port()
	if port == "" {
		if serverURL.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	key := "/library/metadata/" + id
	mediaType := "video"
	if library, ok := s.Libraries[item.LibraryID]; ok {
		switch library.Type {
		case "artist", "album", "track", "music":
			mediaType = "audio"
		case "photo", "image":
			mediaType = "photo"
		}
	}
	return url.Values{
		"key":               {key},
		"path":              {key},
		"machineIdentifier": {s.Resource.ClientIdentifier},
		"address":           {serverURL.Hostname()},
		"port":              {port},
		"protocol":          {serverURL.Scheme},
		"offset":            {strconv.FormatInt(positionMS, 10)},
		"type":              {mediaType},
	}, nil
}

func (a *App) findPlayer(deviceID string) (*plexServer, *plex.PlexClient) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	for _, s := range a.servers {
		if client, ok := s.Clients[deviceID]; ok {
			copy := client
			return s, &copy
		}
	}
	return nil, nil
}

func (a *App) updatePlayerState(addr messenger.Address, state string, position int64, item *domain.MediaItemRef) error {
	key := domain.EntityKey{Plugin: addr.Plugin, DeviceID: addr.DeviceID, ID: addr.EntityID}
	raw, err := a.store.Get(key)
	if err != nil {
		return err
	}
	var entity domain.Entity
	if err := json.Unmarshal(raw, &entity); err != nil {
		return err
	}
	player, _ := entity.State.(domain.MediaPlayer)
	player.State, player.PositionMS, player.LastError = state, position, ""
	if item != nil {
		player.Item = item
	}
	entity.State = player
	return a.store.Save(entity)
}

func (a *App) createRecordingRule(serverID string, cmd domain.RecordingRuleCreate) {
	s := a.server(serverID)
	if s == nil {
		return
	}
	var airingID string
	var series, newOnly bool
	_ = json.Unmarshal(cmd.Rule.Query.Attributes["airingID"], &airingID)
	_ = json.Unmarshal(cmd.Rule.Query.Attributes["series"], &series)
	_ = json.Unmarshal(cmd.Rule.Query.Attributes["newOnly"], &newOnly)
	if airingID == "" {
		a.mu.RLock()
		for id, airing := range s.Airings {
			if cmd.Rule.Query.Title == "" || strings.EqualFold(airing.Metadata.Title, cmd.Rule.Query.Title) {
				airingID = id
				break
			}
		}
		a.mu.RUnlock()
	}
	if airingID == "" {
		log.Printf("plugin-plex: no airing matches recording rule %q", cmd.Rule.Query.Title)
		return
	}
	if cmd.Rule.Selection == "all" {
		series = true
	}
	go a.recordAiring(serverID, airingID, series, newOnly)
}

func (a *App) recordCurrent(addr messenger.Address) {
	s := a.server(addr.DeviceID)
	if s == nil {
		return
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	for id, ch := range s.Channels {
		if "channel-"+safeID(ch.VCN) != addr.EntityID {
			continue
		}
		state := channelState(ch, s.Programs[id], time.Now(), s.GuideAt)
		if state.CurrentProgram != nil {
			go a.recordAiring(addr.DeviceID, state.CurrentProgram.AiringID, false, false)
		}
		return
	}
}

func (a *App) recordAiring(serverID, airingID string, series, newOnly bool) {
	s := a.server(serverID)
	if s == nil {
		return
	}
	a.mu.RLock()
	record, ok := s.Airings[airingID]
	a.mu.RUnlock()
	if !ok {
		return
	}
	guid := record.Metadata.GUID
	if series && record.Metadata.GrandparentGUID != "" {
		guid = record.Metadata.GrandparentGUID
	}
	if guid == "" {
		log.Printf("plugin-plex: recording has no Plex GUID")
		return
	}
	ctx, cancel := context.WithTimeout(a.ctx, 15*time.Second)
	defer cancel()
	var templates plex.Container[plex.Templates]
	if err := s.Client.Get(ctx, "/media/subscriptions/template", url.Values{"guid": {guid}}, &templates); err != nil {
		log.Printf("plugin-plex: load recording template: %v", err)
		return
	}
	wantedType := 4
	if series {
		wantedType = 2
	}
	var selected *plex.Template
	for _, group := range templates.MediaContainer.SubscriptionTemplate {
		for i := range group.MediaSubscription {
			if group.MediaSubscription[i].Type == wantedType {
				candidate := group.MediaSubscription[i]
				selected = &candidate
				break
			}
		}
	}
	if selected == nil {
		log.Printf("plugin-plex: no recording template type %d", wantedType)
		return
	}
	q, err := url.ParseQuery(selected.Parameters)
	if err != nil {
		log.Printf("plugin-plex: parse recording template: %v", err)
		return
	}
	q.Set("type", strconv.Itoa(selected.Type))
	q.Set("targetLibrarySectionID", strconv.Itoa(selected.TargetLibrarySectionID))
	for _, setting := range selected.Setting {
		q.Set("prefs["+setting.ID+"]", fmt.Sprint(setting.Value))
	}
	if series && newOnly {
		q.Set("prefs[onlyNewAirings]", "1")
	}
	if err := s.Client.Post(ctx, "/media/subscriptions", q, nil); err != nil {
		log.Printf("plugin-plex: create recording: %v", err)
		return
	}
	a.syncSubscriptions(ctx, s)
}

func (a *App) cancelExternal(serverID, external string) {
	s := a.server(serverID)
	if s == nil {
		return
	}
	ctx, cancel := context.WithTimeout(a.ctx, 15*time.Second)
	defer cancel()
	if _, err := strconv.Atoi(external); err != nil && strings.HasPrefix(external, "/") {
		external = strings.TrimPrefix(external, "/media/subscriptions/")
	}
	if err := s.Client.Delete(ctx, "/media/subscriptions/"+url.PathEscape(external)); err != nil {
		log.Printf("plugin-plex: cancel recording: %v", err)
		return
	}
	a.syncSubscriptions(ctx, s)
}
