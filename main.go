package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/joho/godotenv"
)

type Config struct {
	APIKey         string
	StopID         string
	Port           string
	AllowedOrigins []string
}

type cacheEntry struct {
	data      any
	expiresAt time.Time
}

type Cache struct {
	mu      sync.RWMutex
	entries map[string]cacheEntry
}

func NewCache() *Cache {
	return &Cache{
		entries: make(map[string]cacheEntry),
	}
}

func (c *Cache) Get(key string) (any, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	entry, ok := c.entries[key]
	if !ok || time.Now().After(entry.expiresAt) {
		return nil, false
	}
	return entry.data, true
}

func (c *Cache) Set(key string, data any, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = cacheEntry{
		data:      data,
		expiresAt: time.Now().Add(ttl),
	}
}

func main() {

	err := godotenv.Load()
	if err != nil {
		slog.Error("Error loading .env file")
	}

	allowedOriginsRaw := os.Getenv("ALLOWED_ORIGINS")
	var allowedOrigins []string
	if allowedOriginsRaw != "" {
		for _, o := range strings.Split(allowedOriginsRaw, ",") {
			if trimmed := strings.TrimSpace(o); trimmed != "" {
				allowedOrigins = append(allowedOrigins, trimmed)
			}
		}
	}

	config := Config{
		APIKey:         os.Getenv("RMV_API_KEY"),
		StopID:         os.Getenv("STOP_ID"),
		Port:           os.Getenv("PORT"),
		AllowedOrigins: allowedOrigins,
	}

	if config.Port == "" {
		config.Port = "8080"
	}

	if config.APIKey == "" {
		slog.Error("RMV_API_KEY environment variable is required")
		os.Exit(1)
	}
	if config.StopID == "" {
		slog.Error("STOP_ID environment variable is required")
		os.Exit(1)
	}

	mux := http.NewServeMux()
	cache := NewCache()

	// Handler for next departures
	mux.HandleFunc("GET /next-departures", func(w http.ResponseWriter, r *http.Request) {
		departures, err := fetchDepartures(r.Context(), cache, config.APIKey, config.StopID)
		if err != nil {
			slog.Error("failed to fetch departures", "error", err)
			http.Error(w, "Failed to fetch departures", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(departures); err != nil {
			slog.Error("failed to encode departures", "error", err)
		}
	})

	// Optional: proxy for the raw departureBoard endpoint if desired,
	// but the requirement says "the created endpoint should list the next departures for a tram stop"
	// and "Only for the departureBoard Endpoint".
	// I'll stick to the specific "next-departures" as requested.

	addr := ":" + config.Port
	slog.Info("Starting server", "addr", addr, "stopId", config.StopID)
	handler := corsMiddleware(mux, config.AllowedOrigins)
	if err := http.ListenAndServe(addr, handler); err != nil {
		slog.Error("server failed", "error", err)
		os.Exit(1)
	}
}

func corsMiddleware(next http.Handler, allowedOrigins []string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && (slices.Contains(allowedOrigins, origin) || slices.Contains(allowedOrigins, "*")) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		}

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func fetchDepartures(ctx context.Context, cache *Cache, apiKey, stopID string) (any, error) {
	cacheKey := stopID
	if data, ok := cache.Get(cacheKey); ok {
		slog.Info("cache hit", "stopId", stopID)
		return data, nil
	}

	u, err := url.Parse("https://www.rmv.de/hapi/departureBoard")
	if err != nil {
		return nil, err
	}

	q := u.Query()
	q.Set("accessId", apiKey)
	q.Set("id", stopID)
	q.Set("format", "json")
	q.Set("duration", "60")
	u.RawQuery = q.Encode()

	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {
			slog.Error("failed to close response body", "error", err)
		}
	}(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API returned status %d", resp.StatusCode)
	}

	var data any
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}

	cache.Set(cacheKey, data, 5*time.Minute)
	slog.Info("fetched new data", "stopId", stopID)

	return data, nil
}
