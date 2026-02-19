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
	"time"

	"github.com/joho/godotenv"
)

type Config struct {
	APIKey string
	StopID string
	Port   string
}

func main() {

	err := godotenv.Load()
	if err != nil {
		slog.Error("Error loading .env file")
	}

	config := Config{
		APIKey: os.Getenv("RMV_API_KEY"),
		StopID: os.Getenv("STOP_ID"),
		Port:   os.Getenv("PORT"),
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

	// Handler for next departures
	mux.HandleFunc("GET /next-departures", func(w http.ResponseWriter, r *http.Request) {
		departures, err := fetchDepartures(r.Context(), config.APIKey, config.StopID)
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
	if err := http.ListenAndServe(addr, mux); err != nil {
		slog.Error("server failed", "error", err)
		os.Exit(1)
	}
}

func fetchDepartures(ctx context.Context, apiKey, stopID string) (any, error) {
	u, err := url.Parse("https://www.rmv.de/hapi/departureBoard")
	if err != nil {
		return nil, err
	}

	q := u.Query()
	q.Set("accessId", apiKey)
	q.Set("id", stopID)
	q.Set("format", "json")
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

	return data, nil
}
