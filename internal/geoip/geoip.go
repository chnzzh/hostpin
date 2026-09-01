package geoip

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"
)

type Location struct {
	CountryCode string  `json:"country_code"`
	Region      string  `json:"region"`
	City        string  `json:"city"`
	Latitude    float64 `json:"latitude"`
	Longitude   float64 `json:"longitude"`
}

type cached struct {
	location Location
	expires  time.Time
}

type Client struct {
	enabled  bool
	provider string
	ttl      time.Duration
	http     *http.Client
	mu       sync.Mutex
	cache    map[string]cached
}

func New(enabled bool, provider string, timeout, ttl time.Duration) *Client {
	return &Client{
		enabled: enabled, provider: provider, ttl: ttl,
		http: &http.Client{Timeout: timeout}, cache: make(map[string]cached),
	}
}

func (c *Client) Configure(enabled bool, provider string) {
	provider = strings.TrimSpace(provider)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.provider != provider {
		clear(c.cache)
	}
	c.enabled, c.provider = enabled, provider
}

func (c *Client) Lookup(ctx context.Context, ip string) (Location, error) {
	c.mu.Lock()
	enabled, provider := c.enabled, c.provider
	c.mu.Unlock()
	if !enabled || provider == "" {
		return Location{}, errors.New("geoip disabled")
	}
	address, err := netip.ParseAddr(ip)
	if err != nil || !address.IsGlobalUnicast() || address.IsPrivate() {
		return Location{}, errors.New("address is not a public unicast address")
	}
	now := time.Now()
	c.mu.Lock()
	entry, ok := c.cache[ip]
	c.mu.Unlock()
	if ok && now.Before(entry.expires) {
		return entry.location, nil
	}
	endpoint := strings.ReplaceAll(provider, "{ip}", url.PathEscape(ip))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Location{}, err
	}
	req.Header.Set("User-Agent", "Hostpin/1 GeoIP")
	response, err := c.http.Do(req)
	if err != nil {
		return Location{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Location{}, fmt.Errorf("geoip provider returned %s", response.Status)
	}
	var payload struct {
		Success     *bool   `json:"success"`
		CountryCode string  `json:"country_code"`
		Region      string  `json:"region"`
		City        string  `json:"city"`
		Latitude    float64 `json:"latitude"`
		Longitude   float64 `json:"longitude"`
		Message     string  `json:"message"`
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20+1))
	if err != nil {
		return Location{}, err
	}
	if len(data) > 1<<20 {
		return Location{}, errors.New("geoip provider response exceeds 1 MiB")
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return Location{}, err
	}
	if payload.Success != nil && !*payload.Success {
		return Location{}, fmt.Errorf("geoip lookup failed: %s", payload.Message)
	}
	location := Location{
		CountryCode: strings.ToUpper(payload.CountryCode), Region: payload.Region,
		City: payload.City, Latitude: payload.Latitude, Longitude: payload.Longitude,
	}
	if len(location.CountryCode) != 2 || location.Latitude < -90 || location.Latitude > 90 || location.Longitude < -180 || location.Longitude > 180 || len(location.Region) > 256 || len(location.City) > 256 {
		return Location{}, errors.New("geoip provider returned an invalid location")
	}
	c.mu.Lock()
	if len(c.cache) >= 4096 {
		for key, value := range c.cache {
			if now.After(value.expires) {
				delete(c.cache, key)
			}
		}
		if len(c.cache) >= 4096 {
			for key := range c.cache {
				delete(c.cache, key)
				break
			}
		}
	}
	c.cache[ip] = cached{location: location, expires: now.Add(c.ttl)}
	c.mu.Unlock()
	return location, nil
}
