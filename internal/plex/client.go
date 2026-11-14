package plex

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const Product = "SlideBolt"

type Client struct {
	BaseURL  string
	Token    string
	ClientID string
	HTTP     *http.Client
}

func New(baseURL, token, clientID string) *Client {
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), Token: token, ClientID: clientID, HTTP: &http.Client{Timeout: 20 * time.Second}}
}

func (c *Client) request(ctx context.Context, method, path string, query url.Values, body io.Reader, out any) error {
	return c.requestTarget(ctx, method, path, query, body, out, "")
}

func (c *Client) requestTarget(ctx context.Context, method, path string, query url.Values, body io.Reader, out any, target string) error {
	u := c.BaseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Plex-Product", Product)
	req.Header.Set("X-Plex-Client-Identifier", c.ClientID)
	if target != "" {
		req.Header.Set("X-Plex-Target-Client-Identifier", target)
	}
	if c.Token != "" {
		req.Header.Set("X-Plex-Token", c.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return fmt.Errorf("plex HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// PlayerCommand sends a Plex Companion command through the media server. The
// server forwards it to the advertised player identified by target.
func (c *Client) PlayerCommand(ctx context.Context, target, path string, query url.Values) error {
	return c.requestTarget(ctx, http.MethodGet, path, query, nil, nil, target)
}

func (c *Client) Get(ctx context.Context, path string, query url.Values, out any) error {
	return c.request(ctx, http.MethodGet, path, query, nil, out)
}

func (c *Client) Post(ctx context.Context, path string, query url.Values, out any) error {
	return c.request(ctx, http.MethodPost, path, query, bytes.NewReader(nil), out)
}

func (c *Client) Delete(ctx context.Context, path string) error {
	return c.request(ctx, http.MethodDelete, path, nil, nil, nil)
}

func (c *Client) Bytes(ctx context.Context, path string) ([]byte, string, error) {
	u := c.BaseURL + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("X-Plex-Product", Product)
	req.Header.Set("X-Plex-Client-Identifier", c.ClientID)
	if c.Token != "" {
		req.Header.Set("X-Plex-Token", c.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("plex HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
	return b, resp.Header.Get("Content-Type"), err
}

type Pin struct {
	ID        int64  `json:"id"`
	Code      string `json:"code"`
	AuthToken string `json:"authToken"`
	ExpiresAt string `json:"expiresAt"`
}

func (c *Client) CreatePin(ctx context.Context) (Pin, error) {
	var p Pin
	err := c.Post(ctx, "/api/v2/pins", url.Values{"strong": {"true"}}, &p)
	return p, err
}

func (c *Client) GetPin(ctx context.Context, id int64) (Pin, error) {
	var p Pin
	err := c.Get(ctx, fmt.Sprintf("/api/v2/pins/%d", id), nil, &p)
	return p, err
}

type Connection struct {
	URI      string `json:"uri"`
	Local    bool   `json:"local"`
	Relay    bool   `json:"relay"`
	Protocol string `json:"protocol"`
}

type Resource struct {
	Name             string       `json:"name"`
	Product          string       `json:"product"`
	ProductVersion   string       `json:"productVersion"`
	ClientIdentifier string       `json:"clientIdentifier"`
	Owned            bool         `json:"owned"`
	Provides         string       `json:"provides"`
	Connections      []Connection `json:"connections"`
}

func (c *Client) Resources(ctx context.Context) ([]Resource, error) {
	var resources []Resource
	err := c.Get(ctx, "/api/v2/resources", url.Values{"includeHttps": {"1"}, "includeRelay": {"1"}}, &resources)
	return resources, err
}

type Container[T any] struct {
	MediaContainer T `json:"MediaContainer"`
}

type Section struct {
	Key   string `json:"key"`
	Type  string `json:"type"`
	Title string `json:"title"`
	Agent string `json:"agent"`
}
type Sections struct {
	Size      int       `json:"size"`
	Directory []Section `json:"Directory"`
}
type LibraryMetadata struct {
	RatingKey             string   `json:"ratingKey"`
	Key                   string   `json:"key"`
	Type                  string   `json:"type"`
	Title                 string   `json:"title"`
	SortTitle             string   `json:"titleSort"`
	Summary               string   `json:"summary"`
	Year                  int      `json:"year"`
	Duration              int64    `json:"duration"`
	ParentRatingKey       string   `json:"parentRatingKey"`
	ParentTitle           string   `json:"parentTitle"`
	GrandparentTitle      string   `json:"grandparentTitle"`
	ParentIndex           int      `json:"parentIndex"`
	Index                 int      `json:"index"`
	Thumb                 string   `json:"thumb"`
	Art                   string   `json:"art"`
	GUID                  string   `json:"guid"`
	GrandparentGUID       string   `json:"grandparentGuid"`
	OriginallyAvailableAt string   `json:"originallyAvailableAt"`
	Media                 []Airing `json:"Media"`
}
type MetadataPage struct {
	Size      int               `json:"size"`
	TotalSize int               `json:"totalSize"`
	Offset    int               `json:"offset"`
	Metadata  []LibraryMetadata `json:"Metadata"`
}
type PlexClient struct {
	Name              string `json:"name"`
	Host              string `json:"host"`
	Port              int    `json:"port"`
	MachineIdentifier string `json:"machineIdentifier"`
	Protocol          string `json:"protocol"`
}
type Clients struct {
	Server []PlexClient `json:"Server"`
}
type SessionPlayer struct {
	Address           string `json:"address"`
	MachineIdentifier string `json:"machineIdentifier"`
	Model             string `json:"model"`
	Platform          string `json:"platform"`
	Product           string `json:"product"`
	State             string `json:"state"`
	Title             string `json:"title"`
}
type SessionUser struct {
	Title string `json:"title"`
}
type Session struct {
	LibraryMetadata
	ViewOffset int64         `json:"viewOffset"`
	Player     SessionPlayer `json:"Player"`
	User       SessionUser   `json:"User"`
	Session    struct {
		ID string `json:"id"`
	} `json:"Session"`
}
type Sessions struct {
	Metadata []Session `json:"Metadata"`
}
type DVR struct {
	Key      string            `json:"key"`
	UUID     string            `json:"uuid"`
	Lineup   string            `json:"lineup"`
	Lineups  []json.RawMessage `json:"Lineup"`
	Language string            `json:"language"`
}
type DVRs struct {
	Dvr []DVR `json:"Dvr"`
}
type Channel struct {
	ID       string          `json:"id"`
	GridKey  string          `json:"gridKey"`
	VCN      string          `json:"vcn"`
	Title    string          `json:"title"`
	CallSign string          `json:"callSign"`
	Thumb    string          `json:"thumb"`
	IsHD     json.RawMessage `json:"isHd"`
}
type Channels struct {
	Channel []Channel `json:"Channel"`
}
type Airing struct {
	ID                json.RawMessage `json:"id"`
	BeginsAt          int64           `json:"beginsAt"`
	EndsAt            int64           `json:"endsAt"`
	ChannelIdentifier string          `json:"channelIdentifier"`
	ChannelVCN        string          `json:"channelVcn"`
	ChannelTitle      string          `json:"channelTitle"`
	GridKey           string          `json:"gridKey"`
}
type Provider struct {
	ID         int    `json:"id"`
	Identifier string `json:"identifier"`
	EPGSource  string `json:"epgSource"`
}
type Providers struct {
	MediaProvider []Provider `json:"MediaProvider"`
}
type Subscription struct {
	Key                    string    `json:"key"`
	Type                   int       `json:"type"`
	Title                  string    `json:"title"`
	TargetLibrarySectionID int       `json:"targetLibrarySectionID"`
	Setting                []Setting `json:"Setting"`
}
type Setting struct {
	ID    string `json:"id"`
	Value any    `json:"value"`
}
type Subscriptions struct {
	MediaSubscription []Subscription `json:"MediaSubscription"`
}
type Grab struct {
	MediaSubscriptionID int             `json:"mediaSubscriptionID"`
	Status              string          `json:"status"`
	Metadata            LibraryMetadata `json:"Metadata"`
}
type Grabs struct {
	MediaGrabOperation []Grab `json:"MediaGrabOperation"`
}
type Template struct {
	Parameters             string    `json:"parameters"`
	Type                   int       `json:"type"`
	TargetLibrarySectionID int       `json:"targetLibrarySectionID"`
	Title                  string    `json:"title"`
	Setting                []Setting `json:"Setting"`
}
type Templates struct {
	SubscriptionTemplate []struct {
		MediaSubscription []Template `json:"MediaSubscription"`
	} `json:"SubscriptionTemplate"`
}

func EncodeID(v string) string          { return strings.TrimRight(base64Raw(v), "=") }
func DecodeID(v string) (string, error) { return base64Decode(v) }
