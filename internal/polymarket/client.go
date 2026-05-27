package polymarket

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	gammaURL string
	clobURL  string
	http     *http.Client
}

func NewClient(gammaURL, clobURL string) *Client {
	return &Client{
		gammaURL: gammaURL,
		clobURL:  clobURL,
		http:     &http.Client{Timeout: 15 * time.Second},
	}
}

func (c *Client) DiscoverMarkets(keywords []string) ([]GammaMarket, error) {
	seen := make(map[string]bool)
	var all []GammaMarket

	// Pass 1: low-volume 5m markets (order by liquidity ascending)
	batches := []struct {
		order     string
		ascending string
		limit     string
	}{
		{"liquidity", "true", "500"},
		{"volume24hr", "false", "500"},
	}

	for _, batch := range batches {
		params := url.Values{}
		params.Set("active", "true")
		params.Set("closed", "false")
		params.Set("limit", batch.limit)
		params.Set("order", batch.order)
		params.Set("ascending", batch.ascending)

		u := fmt.Sprintf("%s/markets?%s", c.gammaURL, params.Encode())

		var body []byte
		var statusCode int
		var fetchErr error
		for attempt := 0; attempt < 3; attempt++ {
			if attempt > 0 {
				backoff := time.Duration(1<<uint(attempt)) * time.Second
				slog.Info("gamma retry", "order", batch.order, "attempt", attempt+1, "backoff", backoff)
				time.Sleep(backoff)
			}
			resp, err := c.http.Get(u)
			if err != nil {
				fetchErr = err
				continue
			}
			body, err = io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				fetchErr = err
				continue
			}
			statusCode = resp.StatusCode
			if statusCode != http.StatusOK {
				fetchErr = fmt.Errorf("http %d", statusCode)
				continue
			}
			fetchErr = nil
			break
		}
		if fetchErr != nil {
			slog.Warn("gamma fetch failed", "order", batch.order, "err", fetchErr)
			continue
		}

		markets, err := parseGammaMarkets(body)
		if err != nil {
			slog.Warn("gamma parse failed", "order", batch.order, "err", err)
			continue
		}

		slog.Info("gamma batch", "order", batch.order, "count", len(markets))

		for _, m := range markets {
			if seen[m.ID] {
				continue
			}
			if !m.Active || m.Closed || !m.EnableOrderBook {
				continue
			}
			if len(m.ClobTokenIDs) < 2 {
				continue
			}
			if matchesKeywords(m, keywords) {
				seen[m.ID] = true
				all = append(all, m)
			}
		}
	}

	slog.Info("discovered markets", "count", len(all))
	for _, m := range all {
		slog.Info("market", "question", m.Question, "slug", m.Slug, "tokens", len(m.ClobTokenIDs), "volume", m.Volume)
	}

	return all, nil
}

func matchesKeywords(m GammaMarket, keywords []string) bool {
	if len(keywords) == 0 {
		return true
	}

	q := strings.ToLower(m.Question)
	slug := strings.ToLower(m.Slug)

	for _, kw := range keywords {
		kwl := strings.ToLower(kw)
		if kwl == "" {
			return true
		}
		if strings.Contains(q, kwl) || strings.Contains(slug, kwl) {
			return true
		}
	}

	return false
}

func (c *Client) GetBook(tokenID string) (*BookSnapshot, error) {
	params := url.Values{}
	params.Set("token_id", tokenID)

	u := fmt.Sprintf("%s/book?%s", c.clobURL, params.Encode())

	resp, err := c.http.Get(u)
	if err != nil {
		return nil, fmt.Errorf("clob GET book: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("clob returned %d", resp.StatusCode)
	}

	var raw ClobBookResponse
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("clob decode book: %w", err)
	}

	snap := &BookSnapshot{
		AssetID: raw.AssetID,
		Market:  raw.Market,
		Hash:    raw.Hash,
	}

	for _, b := range raw.Bids {
		price, _ := parseFloat(b.Price)
		size, _ := parseFloat(b.Size)
		snap.Bids = append(snap.Bids, OrderBookLevel{Price: price, Size: size})
	}

	for _, a := range raw.Asks {
		price, _ := parseFloat(a.Price)
		size, _ := parseFloat(a.Size)
		snap.Asks = append(snap.Asks, OrderBookLevel{Price: price, Size: size})
	}

	return snap, nil
}

func (c *Client) GetMidpoint(tokenID string) (float64, error) {
	params := url.Values{}
	params.Set("token_id", tokenID)

	u := fmt.Sprintf("%s/midpoint?%s", c.clobURL, params.Encode())

	resp, err := c.http.Get(u)
	if err != nil {
		return 0, fmt.Errorf("clob GET midpoint: %w", err)
	}
	defer resp.Body.Close()

	var m MidpointResponse
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return 0, fmt.Errorf("clob decode midpoint: %w", err)
	}

	return parseFloat(m.Midpoint)
}
