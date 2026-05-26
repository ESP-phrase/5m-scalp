package polymarket

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"math/rand"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type WSClient struct {
	url      string
	tokenIDs []string
	conn     *websocket.Conn
	mu       sync.Mutex
	events   chan WSEvent
	done     chan struct{}
	running  bool
}

func NewWSClient(wsURL string, tokenIDs []string) *WSClient {
	return &WSClient{
		url:      wsURL,
		tokenIDs: tokenIDs,
		events:   make(chan WSEvent, 2048),
		done:     make(chan struct{}),
	}
}

func (w *WSClient) Events() <-chan WSEvent {
	return w.events
}

func (w *WSClient) Connect() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	dialer := websocket.Dialer{
		HandshakeTimeout: 15 * time.Second,
	}

	conn, _, err := dialer.Dial(w.url, nil)
	if err != nil {
		return fmt.Errorf("ws dial: %w", err)
	}
	w.conn = conn
	w.running = true

	sub := map[string]interface{}{
		"assets_ids":              w.tokenIDs,
		"type":                    "market",
		"custom_feature_enabled":  true,
	}

	if err := conn.WriteJSON(sub); err != nil {
		return fmt.Errorf("ws subscribe: %w", err)
	}

	slog.Info("webosocket connected", "tokens", len(w.tokenIDs))

	go w.readLoop()
	go w.reconnectLoop()

	return nil
}

func (w *WSClient) readLoop() {
	defer func() {
		w.mu.Lock()
		w.running = false
		w.mu.Unlock()
	}()

	for {
		select {
		case <-w.done:
			return
		default:
		}

		w.mu.Lock()
		conn := w.conn
		w.mu.Unlock()

		if conn == nil {
			time.Sleep(100 * time.Millisecond)
			continue
		}

		_, msg, err := conn.ReadMessage()
		if err != nil {
			slog.Warn("ws read error", "err", err)
			return
		}

		w.parseAndDispatch(msg)
	}
}

func (w *WSClient) reconnectLoop() {
	for {
		select {
		case <-w.done:
			return
		default:
		}

		w.mu.Lock()
		running := w.running
		w.mu.Unlock()

		if !running {
			backoff := time.Duration(1+rand.Intn(5)) * time.Second
			slog.Info("ws reconnecting", "backoff", backoff)

			time.Sleep(backoff)

			if err := w.Connect(); err != nil {
				slog.Warn("ws reconnect failed", "err", err)
			}
		}

		time.Sleep(2 * time.Second)
	}
}

func (w *WSClient) parseAndDispatch(raw []byte) {
	var base struct {
		EventType string `json:"event_type"`
	}
	if err := json.Unmarshal(raw, &base); err != nil {
		return
	}

	evt := WSEvent{
		EventType: base.EventType,
		Raw:       raw,
		Time:      time.Now(),
	}

	switch base.EventType {
	case "book":
		var snap BookSnapshot
		var rawBids []struct {
			Price string `json:"price"`
			Size  string `json:"size"`
		}
		var rawAsks []struct {
			Price string `json:"price"`
			Size  string `json:"size"`
		}

		tmp := struct {
			AssetID   string `json:"asset_id"`
			Market    string `json:"market"`
			Timestamp string `json:"timestamp"`
			Hash      string `json:"hash"`
			Bids      []struct {
				Price string `json:"price"`
				Size  string `json:"size"`
			} `json:"bids"`
			Asks []struct {
				Price string `json:"price"`
				Size  string `json:"size"`
			} `json:"asks"`
		}{}

		if err := json.Unmarshal(raw, &tmp); err != nil {
			return
		}

		snap.AssetID = tmp.AssetID
		snap.Market = tmp.Market
		snap.Hash = tmp.Hash
		snap.Timestamp, _ = parseInt64(tmp.Timestamp)

		for _, b := range tmp.Bids {
			price, _ := parseFloat(b.Price)
			size, _ := parseFloat(b.Size)
			snap.Bids = append(snap.Bids, OrderBookLevel{Price: price, Size: size})
		}
		for _, a := range tmp.Asks {
			price, _ := parseFloat(a.Price)
			size, _ := parseFloat(a.Size)
			snap.Asks = append(snap.Asks, OrderBookLevel{Price: price, Size: size})
		}

		evt.Book = &snap
		evt.AssetID = snap.AssetID
		evt.Market = snap.Market
		rawBids = tmp.Bids
		rawAsks = tmp.Asks
		_ = rawBids
		_ = rawAsks

	case "price_change":
		var pc PriceChange
		if err := json.Unmarshal(raw, &pc); err != nil {
			return
		}
		evt.PriceChg = &pc
		if len(pc.PriceChanges) > 0 {
			evt.AssetID = pc.PriceChanges[0].AssetID
		}
		evt.Market = pc.Market

	case "last_trade_price":
		var tp LastTradePrice
		if err := json.Unmarshal(raw, &tp); err != nil {
			return
		}
		evt.Trade = &tp
		evt.AssetID = tp.AssetID
		evt.Market = tp.Market

	case "best_bid_ask":
		var bba BestBidAsk
		if err := json.Unmarshal(raw, &bba); err != nil {
			return
		}
		evt.BBA = &bba
		evt.AssetID = bba.AssetID
		evt.Market = bba.Market

	default:
		return
	}

	select {
	case w.events <- evt:
	default:
	}
}

func (w *WSClient) Close() {
	close(w.done)
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.conn != nil {
		w.conn.Close()
		w.conn = nil
	}
	w.running = false
}

func parseFloat(s string) (float64, error) {
	if s == "" {
		return 0, nil
	}
	return strconv.ParseFloat(s, 64)
}

func parseInt64(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	return strconv.ParseInt(s, 10, 64)
}

func roundToTick(price, tick float64) float64 {
	if tick <= 0 {
		return price
	}
	return math.Round(price/tick) * tick
}
