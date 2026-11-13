package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"strconv"
	"strings"
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
	case domain.TVRecordCurrent:
		a.recordCurrent(addr)
	case domain.TVRecordAiring:
		a.recordAiring(addr.DeviceID, c.AiringID, false, false)
	case domain.TVRecordSeries:
		a.recordAiring(addr.DeviceID, c.AiringID, true, c.NewOnly && !c.IncludeRepeats)
	case domain.TVRecordCancel:
		a.cancelRecording(addr)
	case domain.MediaPlay, domain.MediaPause, domain.MediaStop, domain.MediaSeek, domain.MediaPlayItem:
		// Plex playback requires selecting a specific advertised client. Entities
		// are still command-capable; unsupported clients remain unchanged rather
		// than leaking their private control URI into state.
	}
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

func (a *App) cancelRecording(addr messenger.Address) {
	s := a.server(addr.DeviceID)
	if s == nil {
		return
	}
	raw, err := a.store.Get(domain.EntityKey{Plugin: PluginID, DeviceID: addr.DeviceID, ID: addr.EntityID})
	if err != nil {
		return
	}
	var envelope struct {
		State json.RawMessage `json:"state"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return
	}
	external := ""
	var rule domain.TVRecordingRule
	if json.Unmarshal(envelope.State, &rule) == nil {
		external = rule.ExternalID
	}
	if external == "" {
		var recording domain.TVRecording
		if json.Unmarshal(envelope.State, &recording) == nil {
			external = recording.ExternalID
		}
	}
	if external == "" {
		external = strings.TrimPrefix(addr.EntityID, "rule-")
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
