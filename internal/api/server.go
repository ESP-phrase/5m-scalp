package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
	"sync"
	"time"

	"scalp5/internal/engine"
	"scalp5/internal/polymarket"
	"scalp5/internal/store"
	"scalp5/internal/strategy"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

type SSEEvent struct {
	Type string      `json:"type"`
	Data interface{} `json:"data"`
	Time string      `json:"time"`
}

type Hub struct {
	mu      sync.RWMutex
	clients map[chan string]struct{}
}

func NewHub() *Hub {
	return &Hub{
		clients: make(map[chan string]struct{}),
	}
}

func (h *Hub) Subscribe() chan string {
	ch := make(chan string, 256)
	h.mu.Lock()
	h.clients[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *Hub) Unsubscribe(ch chan string) {
	h.mu.Lock()
	delete(h.clients, ch)
	h.mu.Unlock()
}

func (h *Hub) Broadcast(event SSEEvent) {
	data, err := json.Marshal(event)
	if err != nil {
		return
	}
	msg := fmt.Sprintf("event: %s\ndata: %s\n\n", event.Type, data)

	h.mu.RLock()
	defer h.mu.RUnlock()
	for ch := range h.clients {
		select {
		case ch <- msg:
		default:
		}
	}
}

type Server struct {
	hub          *Hub
	store        *store.Store
	paperEngine  *engine.PaperEngine
	pnlTracker   *engine.PnLTracker
	strategyReg  *strategy.Registry
	book         *engine.VirtualBook
	router       chi.Router
	running      bool
	mu           sync.Mutex
	markets      []polymarket.GammaMarket
	eventsCh     chan<- interface{}
	onRunningChanged func(bool)
}

func NewServer(
	hub *Hub,
	st *store.Store,
	pe *engine.PaperEngine,
	pnl *engine.PnLTracker,
	reg *strategy.Registry,
	book *engine.VirtualBook,
) *Server {
	s := &Server{
		hub:         hub,
		store:       st,
		paperEngine: pe,
		pnlTracker:  pnl,
		strategyReg: reg,
		book:        book,
		running:     true,
	}

	r := chi.NewRouter()

	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(corsMiddleware)

	r.Get("/api/events", s.handleSSE)
	r.Get("/api/stats", s.handleStats)
	r.Post("/api/bot/start", s.handleStart)
	r.Post("/api/bot/stop", s.handleStop)
	r.Post("/api/bot/strategy", s.handleSwitchStrategy)
	r.Get("/api/markets", s.handleMarkets)
	r.Get("/api/fills", s.handleFills)
	r.Post("/api/orders/cancel-all", s.handleCancelAllOrders)
	r.Post("/api/orders/{id}/cancel", s.handleCancelOrder)

	s.router = r
	return s
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) Handler() http.Handler {
	return s.router
}

func (s *Server) SetRunningCallback(fn func(bool)) {
	s.onRunningChanged = fn
}

func (s *Server) RunningCallback() func(bool) {
	return s.onRunningChanged
}

func (s *Server) Stop() {
	s.mu.Lock()
	s.running = false
	s.mu.Unlock()
	if s.onRunningChanged != nil {
		s.onRunningChanged(false)
	}
	slog.Info("bot stopped")
	s.hub.Broadcast(SSEEvent{
		Type: "log",
		Data: map[string]string{"message": "Bot stopped"},
		Time: time.Now().Format(time.RFC3339),
	})
}

func (s *Server) IsRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

func (s *Server) SetEventsChannel(ch chan interface{}) {
	s.eventsCh = ch
}

func (s *Server) SetMarkets(markets []polymarket.GammaMarket) {
	s.mu.Lock()
	s.markets = markets
	s.mu.Unlock()
}

func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	ch := s.hub.Subscribe()
	defer s.hub.Unsubscribe(ch)

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case msg := <-ch:
			fmt.Fprint(w, msg)
			flusher.Flush()
		case <-ticker.C:
			state := s.pnlTracker.GetState()
			evt := SSEEvent{
				Type: "pnl",
				Data: state,
				Time: time.Now().Format(time.RFC3339),
			}
			data, _ := json.Marshal(evt)
			fmt.Fprintf(w, "event: pnl\ndata: %s\n\n", data)
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	state := s.pnlTracker.GetState()
	openOrders := s.paperEngine.GetOpenOrders()
	positions := s.pnlTracker.GetPositions()

	midpoints := make(map[string]float64)
	for _, o := range openOrders {
		if _, ok := midpoints[o.TokenID]; !ok {
			midpoints[o.TokenID] = s.book.Midpoint(o.TokenID)
		}
	}

	resp := map[string]interface{}{
		"pnl":         state,
		"open_orders": openOrders,
		"positions":   positions,
		"running":     s.running,
		"midpoints":   midpoints,
	}

	active := s.strategyReg.Active()
	if active != nil {
		resp["strategy"] = active.Name()
	}

	writeJSON(w, resp)
}

func (s *Server) handleStart(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.running = true
	s.mu.Unlock()

	if s.onRunningChanged != nil {
		s.onRunningChanged(true)
	}

	active := s.strategyReg.Active()
	name := ""
	if active != nil {
		name = active.Name()
	}

	slog.Info("bot started", "strategy", name)

	s.hub.Broadcast(SSEEvent{
		Type: "log",
		Data: map[string]string{"message": "Bot started with strategy: " + name},
		Time: time.Now().Format(time.RFC3339),
	})

	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.running = false
	s.mu.Unlock()

	if s.onRunningChanged != nil {
		s.onRunningChanged(false)
	}

	slog.Info("bot stopped")
	s.hub.Broadcast(SSEEvent{
		Type: "log",
		Data: map[string]string{"message": "Bot stopped"},
		Time: time.Now().Format(time.RFC3339),
	})

	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleSwitchStrategy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, map[string]interface{}{"ok": false, "error": "invalid body"})
		return
	}

	ok := s.strategyReg.SetActive(req.Name)
	if !ok {
		writeJSON(w, map[string]interface{}{"ok": false, "error": "strategy not found"})
		return
	}

	s.hub.Broadcast(SSEEvent{
		Type: "log",
		Data: map[string]string{"message": "Switched to strategy: " + req.Name},
		Time: time.Now().Format(time.RFC3339),
	})

	writeJSON(w, map[string]interface{}{"ok": true, "strategy": req.Name})
}

func (s *Server) handleMarkets(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	type marketSummary struct {
		ID       string   `json:"id"`
		Question string   `json:"question"`
		Tokens   []string `json:"token_ids"`
		Midpoint float64   `json:"midpoint"`
	}

	summaries := make([]marketSummary, 0, len(s.markets))
	for _, m := range s.markets {
		mid := 0.0
		if len(m.ClobTokenIDs) > 0 {
			mid = s.book.Midpoint(m.ClobTokenIDs[0])
		}
		summaries = append(summaries, marketSummary{
			ID:       m.ID,
			Question: m.Question,
			Tokens:   m.ClobTokenIDs,
			Midpoint: round2(mid),
		})
	}

	writeJSON(w, summaries)
}

func (s *Server) handleFills(w http.ResponseWriter, r *http.Request) {
	fills := s.pnlTracker.GetRecentFills(50)

	type fillJSON struct {
		OrderID string  `json:"order_id"`
		TokenID string  `json:"token_id"`
		Side    string  `json:"side"`
		Price   float64 `json:"price"`
		Size    float64 `json:"size"`
		Time    string  `json:"time"`
	}

	out := make([]fillJSON, len(fills))
	for i, f := range fills {
		out[i] = fillJSON{
			OrderID: f.OrderID,
			TokenID: f.TokenID,
			Side:    f.Side,
			Price:   f.Price,
			Size:    f.Size,
			Time:    f.Time.Format(time.RFC3339),
		}
	}

	writeJSON(w, out)
}

func (s *Server) handleCancelAllOrders(w http.ResponseWriter, r *http.Request) {
	count := s.paperEngine.CancelAllOrders()
	if mm, ok := s.strategyReg.Active().(*strategy.MarketMakingStrategy); ok {
		mm.RemoveAllOrders()
	}
	s.hub.Broadcast(SSEEvent{
		Type: "log",
		Data: map[string]string{"message": fmt.Sprintf("Cancelled %d orders", count)},
		Time: time.Now().Format(time.RFC3339),
	})
	writeJSON(w, map[string]interface{}{"ok": true, "count": count})
}

func (s *Server) handleCancelOrder(w http.ResponseWriter, r *http.Request) {
	orderID := chi.URLParam(r, "id")
	if orderID == "" {
		writeJSON(w, map[string]interface{}{"ok": false, "error": "missing order id"})
		return
	}

	if s.paperEngine.CancelOrder(orderID) {
		if mm, ok := s.strategyReg.Active().(*strategy.MarketMakingStrategy); ok {
			mm.RemoveOrder(orderID)
		}
		s.hub.Broadcast(SSEEvent{
			Type: "log",
			Data: map[string]string{"message": "Order cancelled: " + orderID},
			Time: time.Now().Format(time.RFC3339),
		})
		writeJSON(w, map[string]bool{"ok": true})
	} else {
		writeJSON(w, map[string]interface{}{"ok": false, "error": "order not found or already filled"})
	}
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func round2(v float64) float64 {
	return float64(int(v*100+0.5)) / 100
}

func init() {
	_ = rand.Intn
}
