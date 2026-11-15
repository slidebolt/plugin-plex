package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/slidebolt/plugin-plex/internal/plex"
	contract "github.com/slidebolt/sb-contract"
	domain "github.com/slidebolt/sb-domain"
	media "github.com/slidebolt/sb-media-sdk"
	messenger "github.com/slidebolt/sb-messenger-sdk"
	storage "github.com/slidebolt/sb-storage-sdk"
)

const PluginID = "plugin-plex"

type authData struct {
	Token    string `json:"token"`
	ClientID string `json:"clientID"`
}
type airingRecord struct {
	ChannelID string
	Program   domain.TVProgram
	Metadata  plex.LibraryMetadata
}
type plexServer struct {
	ID           string
	Resource     plex.Resource
	Client       *plex.Client
	ProviderPath string
	Libraries    map[string]plex.Section
	Channels     map[string]plex.Channel
	Programs     map[string][]domain.TVProgram
	Airings      map[string]airingRecord
	Clients      map[string]plex.PlexClient
	GuideAt      time.Time
}

type App struct {
	msg     messenger.Messenger
	store   storage.Storage
	cmds    *messenger.Commands
	subs    []messenger.Subscription
	config  config
	ctx     context.Context
	cancel  context.CancelFunc
	mu      sync.RWMutex
	auth    authData
	servers map[string]*plexServer
}

func New() *App { return &App{servers: map[string]*plexServer{}} }
func (a *App) Hello() contract.HelloResponse {
	definitions := contract.DescribePlugin("Plex", "Supplies media catalogs, players, activity, television sources, and generic recording schedules.", "media_library", "media_player", "media_provider_connection", "media_server", "media_session", "recorder", "recording_rule", "schedule", "tv_channel")
	definitions.Collections = []contract.CollectionDefinition{
		{ID: "sessions", Name: "Playback sessions", Description: "Playback that is active on the media server now.", EntityTypes: []string{"media_session"}, Mode: contract.CollectionAuthoritativeSnapshot, OnMissing: contract.CollectionMissingDelete},
		{ID: "recording-rules", Name: "Recording rules", Description: "Provider-owned rules that select future media for recording.", EntityTypes: []string{"recording_rule"}, Mode: contract.CollectionAuthoritativeSnapshot, OnMissing: contract.CollectionMissingDelete},
		{ID: "scheduled-recordings", Name: "Scheduled recordings", Description: "Concrete future recordings produced by recording rules.", EntityTypes: []string{"schedule"}, Mode: contract.CollectionAuthoritativeSnapshot, OnMissing: contract.CollectionMissingDelete},
	}
	return contract.HelloResponse{ID: PluginID, Kind: contract.KindPlugin, ContractVersion: contract.ContractVersion, DependsOn: []string{"messenger", "storage"}, Definitions: definitions, Setup: contract.SelectableSetup("Media", contract.SetupField{Key: "refresh_interval", Environment: "PLEX_REFRESH_INTERVAL", Name: "Refresh interval", Description: "Minimum interval between Plex inventory refreshes.", Control: contract.SetupText, Advanced: true, Default: json.RawMessage(`"5m"`)})}
}

func (a *App) OnStart(deps map[string]json.RawMessage) (json.RawMessage, error) {
	msg, err := messenger.Connect(deps)
	if err != nil {
		return nil, err
	}
	store, err := storage.Connect(deps)
	if err != nil {
		msg.Close()
		return nil, err
	}
	a.msg, a.store, a.config = msg, store, loadConfig()
	a.ctx, a.cancel = context.WithCancel(context.Background())
	a.cmds = messenger.NewCommands(msg, domain.LookupCommand)
	sub, err := a.cmds.Receive(PluginID+".>", a.handleCommand)
	if err != nil {
		return nil, err
	}
	a.subs = append(a.subs, sub)
	providerSubs, err := media.ServeProvider(msg, PluginID, a)
	if err != nil {
		return nil, err
	}
	a.subs = append(a.subs, providerSubs...)
	if err := a.ensureAccount(); err != nil {
		return nil, err
	}
	a.loadAuth()
	if a.auth.Token != "" {
		go a.reconcile(a.ctx)
	}
	go a.loop()
	log.Println("plugin-plex: started")
	return nil, nil
}

func (a *App) OnShutdown() error {
	if a.cancel != nil {
		a.cancel()
	}
	for _, sub := range a.subs {
		_ = sub.Unsubscribe()
	}
	if a.store != nil {
		a.store.Close()
	}
	if a.msg != nil {
		a.msg.Close()
	}
	return nil
}

func (a *App) loop() {
	t := time.NewTicker(a.config.Refresh)
	defer t.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-t.C:
			if a.auth.Token != "" {
				a.reconcile(a.ctx)
			}
		}
	}
}

func (a *App) ensureAccount() error {
	device := accountDevice()
	if err := a.store.Save(device); err != nil {
		return err
	}
	return a.saveConnection(domain.MediaProviderConnection{Status: "disconnected"})
}

func accountDevice() domain.Device {
	return domain.Device{ID: "account", Plugin: PluginID, Name: "Plex Account", Entities: []domain.Entity{}}
}

func (a *App) loadAuth() {
	raw, err := a.store.GetPrivate(domain.Device{ID: "account", Plugin: PluginID})
	if err == nil {
		_ = json.Unmarshal(raw, &a.auth)
	}
	if a.auth.ClientID == "" {
		a.auth.ClientID = randomID()
	}
	if a.auth.Token != "" {
		_ = a.saveConnection(domain.MediaProviderConnection{Status: "connected"})
	}
}

func randomID() string { b := make([]byte, 16); _, _ = rand.Read(b); return hex.EncodeToString(b) }

func (a *App) saveConnection(state domain.MediaProviderConnection) error {
	return a.store.Save(domain.Entity{ID: "connection", Plugin: PluginID, DeviceID: "account", Type: "media_provider_connection", Name: "Plex Connection", Commands: []string{"media_provider_connect", "media_provider_disconnect"}, State: state, Status: connectionHealth(state)})
}

func (a *App) connect() {
	clientID := a.auth.ClientID
	if clientID == "" {
		clientID = randomID()
	}
	cloud := plex.New("https://plex.tv", "", clientID)
	pin, err := cloud.CreatePin(a.ctx)
	if err != nil {
		_ = a.saveConnection(domain.MediaProviderConnection{Status: "error", LastError: err.Error()})
		return
	}
	expires, _ := time.Parse(time.RFC3339, pin.ExpiresAt)
	claim := fmt.Sprintf("https://app.plex.tv/auth#?clientID=%s&code=%s&context%%5Bdevice%%5D%%5Bproduct%%5D=SlideBolt", url.QueryEscape(clientID), url.QueryEscape(pin.Code))
	_ = a.saveConnection(domain.MediaProviderConnection{Status: "pending", ClaimURL: claim, Code: pin.Code, ExpiresAt: &expires})
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-ticker.C:
			current, err := cloud.GetPin(a.ctx, pin.ID)
			if err != nil {
				_ = a.saveConnection(domain.MediaProviderConnection{Status: "error", LastError: err.Error()})
				return
			}
			if current.AuthToken != "" {
				a.mu.Lock()
				a.auth = authData{Token: current.AuthToken, ClientID: clientID}
				auth := a.auth
				a.mu.Unlock()
				raw, _ := json.Marshal(auth)
				if err := a.store.SetPrivate(domain.Device{ID: "account", Plugin: PluginID}, raw); err != nil {
					_ = a.saveConnection(domain.MediaProviderConnection{Status: "error", LastError: err.Error()})
					return
				}
				_ = a.saveConnection(domain.MediaProviderConnection{Status: "connected"})
				a.reconcile(a.ctx)
				return
			}
			if !expires.IsZero() && time.Now().After(expires) {
				_ = a.saveConnection(domain.MediaProviderConnection{Status: "error", LastError: "Plex PIN expired"})
				return
			}
		}
	}
}

func (a *App) disconnect() {
	a.mu.Lock()
	a.auth.Token = ""
	a.servers = map[string]*plexServer{}
	a.mu.Unlock()
	_ = a.store.DeletePrivate(domain.Device{ID: "account", Plugin: PluginID})
	_ = a.saveConnection(domain.MediaProviderConnection{Status: "disconnected"})
}

func (a *App) reconcile(ctx context.Context) {
	a.mu.RLock()
	auth := a.auth
	a.mu.RUnlock()
	if auth.Token == "" {
		return
	}
	cloud := plex.New("https://plex.tv", auth.Token, auth.ClientID)
	resources, err := cloud.Resources(ctx)
	if err != nil {
		_ = a.saveConnection(domain.MediaProviderConnection{Status: "error", LastError: err.Error()})
		return
	}
	seen := map[string]bool{}
	for _, resource := range resources {
		if resource.Product != "Plex Media Server" || !strings.Contains(resource.Provides, "server") {
			continue
		}
		base := chooseConnection(ctx, resource, auth)
		if base == "" {
			continue
		}
		id := safeID(resource.ClientIdentifier)
		seen[id] = true
		s := &plexServer{ID: id, Resource: resource, Client: plex.New(base, auth.Token, auth.ClientID), Libraries: map[string]plex.Section{}, Channels: map[string]plex.Channel{}, Programs: map[string][]domain.TVProgram{}, Airings: map[string]airingRecord{}, Clients: map[string]plex.PlexClient{}}
		a.mu.Lock()
		if old := a.servers[id]; old != nil {
			s.GuideAt, s.Programs, s.Airings, s.Channels, s.ProviderPath, s.Clients = old.GuideAt, old.Programs, old.Airings, old.Channels, old.ProviderPath, old.Clients
		}
		a.servers[id] = s
		a.mu.Unlock()
		a.syncServer(ctx, s)
	}
	_ = seen
	_ = a.saveConnection(domain.MediaProviderConnection{Status: "connected"})
}

func chooseConnection(ctx context.Context, r plex.Resource, auth authData) string {
	conns := append([]plex.Connection(nil), r.Connections...)
	sort.SliceStable(conns, func(i, j int) bool {
		if conns[i].Local != conns[j].Local {
			return conns[i].Local
		}
		if conns[i].Protocol != conns[j].Protocol {
			return conns[i].Protocol == "https"
		}
		return !conns[i].Relay
	})
	for _, c := range conns {
		client := plex.New(c.URI, auth.Token, auth.ClientID)
		var out map[string]any
		probe, cancel := context.WithTimeout(ctx, 3*time.Second)
		err := client.Get(probe, "/identity", nil, &out)
		cancel()
		if err == nil {
			return c.URI
		}
	}
	return ""
}

func (a *App) syncServer(ctx context.Context, s *plexServer) {
	now := time.Now().UTC()
	_ = a.store.Save(a.healthyDevice(domain.Device{ID: s.ID, Plugin: PluginID, Name: s.Resource.Name}))
	_ = a.store.Save(a.healthyEntity(domain.Entity{ID: "server", Plugin: PluginID, DeviceID: s.ID, Type: "media_server", Name: s.Resource.Name, State: domain.MediaServer{Connected: true, Name: s.Resource.Name, Version: s.Resource.ProductVersion, Owned: s.Resource.Owned, LastSyncAt: &now}}))
	a.syncLibraries(ctx, s, now)
	a.syncClients(ctx, s)
	a.syncSessions(ctx, s)
	a.syncDVR(ctx, s, now)
}

func safeID(v string) string {
	v = strings.ToLower(v)
	var b strings.Builder
	for _, r := range v {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}
