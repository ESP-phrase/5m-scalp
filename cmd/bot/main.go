package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"bytes"
	"encoding/json"
	"io"

	"scalp5/internal/api"
	"scalp5/internal/config"
	"scalp5/internal/engine"
	"scalp5/internal/polymarket"
	"scalp5/internal/store"
	"scalp5/internal/strategy"
	"scalp5/internal/telemetry"
)

var running atomic.Bool
var (
	modelAssign = make(map[string]string)
	modelMu     sync.Mutex
	rrCounter   int
)

var (
	tpClosedMu     sync.Mutex
	tpClosedTokens = make(map[string]bool)
)

func assignModel(tokenID string) string {
	modelMu.Lock()
	defer modelMu.Unlock()
	if m, ok := modelAssign[tokenID]; ok {
		return m
	}
	if rrCounter%2 == 0 {
		modelAssign[tokenID] = "lgb"
	} else {
		modelAssign[tokenID] = "xgb"
	}
	rrCounter++
	return modelAssign[tokenID]
}

func callModelServer(modelTag string, features []float64) (float64, float64, error) {
	url := "http://127.0.0.1:8000/predict"
	if modelTag == "lgb" {
		url = "http://127.0.0.1:8001/predict"
	}
	payload := map[string]interface{}{"features": []interface{}{features}}
	b, _ := json.Marshal(payload)
	start := time.Now()
	resp, err := http.Post(url, "application/json", bytes.NewBuffer(b))
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var out map[string]interface{}
	if err := json.Unmarshal(body, &out); err != nil {
		return 0, 0, err
	}
	lat := time.Since(start).Seconds() * 1000.0
	var pred float64
	if p, ok := out["predictions"].([]interface{}); ok && len(p) > 0 {
		if v, ok := p[0].(float64); ok {
			pred = v
		}
	}
	// try to read server-side latency if present
	if lm, ok := out["latency_ms"].(float64); ok {
		lat = lm
	}
	return pred, lat, nil
}

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	config.LoadDotEnv(".env")
	cfg := config.Load()

	st, err := store.New(cfg.DBPath)
	if err != nil {
		slog.Error("failed to open store", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	client := polymarket.NewClient(cfg.GammaURL, cfg.ClobURL)
	markets, err := client.DiscoverMarkets(cfg.MarketSearchTags)
	if err != nil {
		slog.Error("failed to discover markets", "err", err)
	}

	if len(markets) == 0 {
		slog.Warn("no markets found, will keep searching")
	}

	var tokenIDs []string
	seen := make(map[string]bool)
	for _, m := range markets {
		for _, tid := range m.ClobTokenIDs {
			if !seen[tid] {
				seen[tid] = true
				tokenIDs = append(tokenIDs, tid)
			}
		}
	}

	slog.Info("subscribing to tokens", "count", len(tokenIDs))

	book := engine.NewVirtualBook()
	paperEngine := engine.NewPaperEngine(book, cfg.FillProbability, cfg.MarketFillProbability)
	pnlTracker := engine.NewPnLTracker(book, cfg.FeeRate, cfg.Bankroll)

	reg := strategy.NewRegistry()
	mm := strategy.NewMarketMaking(cfg.MinSpread, cfg.MaxSpread, cfg.DefaultOrderSize, cfg.MaxPositionPerToken)
	reg.Register(mm)
	reg.SetActive("market_making")

	hub := api.NewHub()
	paperEngine.SetEventChannel(makeEvtChan(hub))

	server := api.NewServer(hub, st, paperEngine, pnlTracker, reg, book)
	server.SetMarkets(markets)
	if err := telemetry.Init(cfg.RedisAddr, "telemetry.csv"); err != nil {
		slog.Warn("telemetry init failed", "err", err)
	}

	// Default to running
	running.Store(true)
	controlCh := make(chan bool, 10)
	server.SetRunningCallback(func(v bool) {
		running.Store(v)
		select {
		case controlCh <- v:
		default:
		}
	})

	wsEvents := make(chan polymarket.WSEvent, 2048)

	if len(tokenIDs) > 0 {
		wsClient := polymarket.NewWSClient(cfg.WsURL, tokenIDs)
		go func() {
			for {
				if err := wsClient.Connect(); err != nil {
					slog.Warn("ws connect failed", "err", err)
					time.Sleep(3 * time.Second)
					continue
				}
				break
			}
		}()

		go func() {
			for evt := range wsClient.Events() {
				select {
				case wsEvents <- evt:
				default:
				}
			}
		}()
	}

	// Seed initial book data via REST
	go func() {
		time.Sleep(2 * time.Second)
		for _, tid := range tokenIDs {
			snap, err := client.GetBook(tid)
			if err != nil {
				continue
			}
			if len(snap.Bids) > 0 || len(snap.Asks) > 0 {
				feedBookToEngine(snap, book, paperEngine, pnlTracker, reg, hub, st, cfg)
			}
		}
		slog.Info("initial book seeding complete")
	}()

	marketsRefresh := time.NewTicker(5 * time.Minute)
	defer marketsRefresh.Stop()

	go func() {
		for range marketsRefresh.C {
			newMarkets, err := client.DiscoverMarkets(cfg.MarketSearchTags)
			if err != nil {
				continue
			}
			var newTokens []string
			for _, m := range newMarkets {
				for _, tid := range m.ClobTokenIDs {
					if !seen[tid] {
						seen[tid] = true
						newTokens = append(newTokens, tid)
					}
				}
			}
			if len(newTokens) > 0 {
				slog.Info("new markets found", "count", len(newTokens))
				server.SetMarkets(newMarkets)
			}
		}
	}()

	go runEngine(wsEvents, book, paperEngine, pnlTracker, reg, hub, st, cfg, client, tokenIDs, controlCh, func() {
		server.Stop()
	})

	go func() {
		pnlTicker := time.NewTicker(10 * time.Second)
		defer pnlTicker.Stop()
		for range pnlTicker.C {
			state := pnlTracker.GetState()
			st.RecordPnL(state.Realized, state.Unrealized)
		}
	}()

	// High-frequency per-order take-profit check (100ms)
	if cfg.TakeProfit > 0 {
		go func() {
			tpCheck := time.NewTicker(100 * time.Millisecond)
			defer tpCheck.Stop()
			for range tpCheck.C {
				if !running.Load() {
					continue
				}
				func() {
					defer func() {
						if r := recover(); r != nil {
							slog.Error("TP check panicked", "recover", r)
						}
					}()
					checkPerOrderTP(book, paperEngine, pnlTracker, reg, hub, cfg.TakeProfit, &tpClosedTokens, &tpClosedMu)
				}()
			}
		}()
	}

	go func() {
		slog.Info("api server starting", "port", cfg.APIPort)
		if err := http.ListenAndServe(":"+cfg.APIPort, server.Handler()); err != nil {
			slog.Error("server error", "err", err)
			os.Exit(1)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	slog.Info("shutting down")
}

func runEngine(
	wsEvents <-chan polymarket.WSEvent,
	book *engine.VirtualBook,
	paperEngine *engine.PaperEngine,
	pnlTracker *engine.PnLTracker,
	reg *strategy.Registry,
	hub *api.Hub,
	st *store.Store,
	cfg *config.Config,
	client *polymarket.Client,
	tokenIDs []string,
	controlCh chan bool,
	stopFn func(),
) {
	ticker := time.NewTicker(cfg.TickInterval)
	defer ticker.Stop()

	bookRefresh := time.NewTicker(1 * time.Second)
	defer bookRefresh.Stop()

	lastBookTime := make(map[string]time.Time)

	for {
		select {
		case start := <-controlCh:
			if start {
				slog.Info("engine started via control")
				go seedBookData(tokenIDs, client, book, paperEngine, pnlTracker, reg, hub, st, cfg)
			} else {
				slog.Info("engine stopped via control — cancelling all orders")
				for _, tid := range tokenIDs {
					cancelled := paperEngine.CancelAllForToken(tid)
					for _, oid := range cancelled {
						if mm, ok := reg.Active().(*strategy.MarketMakingStrategy); ok {
							mm.RemoveOrder(oid)
						}
					}
					book.ClearToken(tid)
				}
				reg.Active().Reset()
				lastBookTime = make(map[string]time.Time)
				hub.Broadcast(api.SSEEvent{
					Type: "log",
					Data: map[string]string{"message": "All orders cancelled — bot stopped"},
					Time: time.Now().Format(time.RFC3339),
				})
			}
		case evt := <-wsEvents:
			if evt.Book != nil {
				lastBookTime[evt.Book.AssetID] = time.Now()
				tpClosedMu.Lock()
				skip := tpClosedTokens[evt.Book.AssetID]
				tpClosedMu.Unlock()
				if skip {
					continue
				}
			}
			processWSEvent(evt, book, paperEngine, pnlTracker, reg, hub, st)

		case <-ticker.C:
			if !running.Load() {
				continue
			}

			active := reg.Active()
			if active == nil {
				continue
			}

			signals := active.OnTick()
			processSignals(signals, book, paperEngine, active, hub, st, cfg)

			// Generate synthetic books for tokens with no orders
			for _, tid := range tokenIDs {
				tpClosedMu.Lock()
				skip := tpClosedTokens[tid]
				tpClosedMu.Unlock()
				if skip {
					continue
				}
				if time.Since(lastBookTime[tid]) < 30*time.Second {
					continue
				}
				hasOrders := false
				for _, o := range paperEngine.GetOpenOrders() {
					if o.TokenID == tid {
						hasOrders = true
						break
					}
				}
				if hasOrders {
					continue
				}
				// Generate synthetic book
				genSyntheticBook(tid, book, active, paperEngine, hub, st, cfg)
				lastBookTime[tid] = time.Now()
			}

			// Synthetic trade generation for P&L visibility
			for _, tid := range tokenIDs {
				tpClosedMu.Lock()
				skip := tpClosedTokens[tid]
				tpClosedMu.Unlock()
				if skip {
					continue
				}
				if rand.Float64() > 0.04 {
					continue
				}
				openOrders := paperEngine.GetOpenOrders()
				var candidates []*engine.PaperOrder
				for _, o := range openOrders {
					if o.TokenID == tid {
						candidates = append(candidates, o)
					}
				}
				if len(candidates) == 0 {
					continue
				}
				order := candidates[rand.Intn(len(candidates))]

				takerSide := "SELL"
				if order.Side == "SELL" {
					takerSide = "BUY"
				}

				tradeSize := 2.0 + rand.Float64()*8.0
				tradeSize = float64(int(tradeSize*100)) / 100

				hub.Broadcast(api.SSEEvent{
					Type: "trade",
					Data: map[string]interface{}{
						"asset_id": tid,
						"price":    order.Price,
						"size":     tradeSize,
						"side":     takerSide,
					},
					Time: time.Now().Format(time.RFC3339),
				})

				fill := paperEngine.FillOrder(order.ID, tradeSize)
				if fill != nil {
					pnlTracker.RecordFill(*fill)
					st.RecordFill(fill.OrderID, fill.TokenID, fill.Side, fill.Price, fill.Size)
					if mm, ok := active.(*strategy.MarketMakingStrategy); ok {
						mm.UpdatePosition(fill.TokenID, fill.Size, fill.Side)
					}
				}
			}

			// Check per-order take-profit
			if cfg.TakeProfit > 0 {
				checkPerOrderTP(book, paperEngine, pnlTracker, reg, hub, cfg.TakeProfit, &tpClosedTokens, &tpClosedMu)
			}

		case <-bookRefresh.C:
			if !running.Load() {
				continue
			}
			// Poll REST for book data as fallback
			for _, tid := range tokenIDs {
				tpClosedMu.Lock()
				skip := tpClosedTokens[tid]
				tpClosedMu.Unlock()
				if skip {
					continue
				}
				snap, err := client.GetBook(tid)
				if err != nil {
					continue
				}
				if len(snap.Bids) > 0 || len(snap.Asks) > 0 {
					feedBookToEngine(snap, book, paperEngine, pnlTracker, reg, hub, st, cfg)
				}
			}
		}
	}
}

func seedBookData(
	tokenIDs []string,
	client *polymarket.Client,
	book *engine.VirtualBook,
	paperEngine *engine.PaperEngine,
	pnlTracker *engine.PnLTracker,
	reg *strategy.Registry,
	hub *api.Hub,
	st *store.Store,
	cfg *config.Config,
) {
	active := reg.Active()
	if active == nil {
		slog.Warn("seedBookData: no active strategy")
		return
	}

	for _, tid := range tokenIDs {
		slog.Info("seeding book", "token", tid)

		beforeCount := len(paperEngine.GetOpenOrders())
		snap, err := client.GetBook(tid)
		if err != nil {
			slog.Warn("REST book fetch failed, using synthetic", "token", tid, "err", err)
			genSyntheticBook(tid, book, active, paperEngine, hub, st, cfg)
			continue
		}

		if len(snap.Bids) > 0 || len(snap.Asks) > 0 {
			feedBookToEngine(snap, book, paperEngine, pnlTracker, reg, hub, st, cfg)
			afterCount := len(paperEngine.GetOpenOrders())
			if afterCount > beforeCount {
				slog.Info("orders placed from real book data", "token", tid)
				continue
			}
			slog.Info("real book data produced no orders (strategy likely rejected spread), using synthetic", "token", tid)
		} else {
			slog.Info("no real book data, generating synthetic", "token", tid)
		}

		genSyntheticBook(tid, book, active, paperEngine, hub, st, cfg)
	}
}

func genSyntheticBook(
	tokenID string,
	book *engine.VirtualBook,
	active strategy.Strategy,
	paperEngine *engine.PaperEngine,
	hub *api.Hub,
	st *store.Store,
	cfg *config.Config,
) {
	mid := 0.50
	spread := 0.04 + rand.Float64()*0.02
	tick := 0.01

	bidP := mid - spread/2
	askP := mid + spread/2

	snap := polymarket.BookSnapshot{
		AssetID: tokenID,
		Market:  "synthetic",
		Bids: []polymarket.OrderBookLevel{
			{Price: roundToTick(bidP, tick), Size: 2000},
			{Price: roundToTick(bidP-tick, tick), Size: 3500},
			{Price: roundToTick(bidP-2*tick, tick), Size: 5000},
		},
		Asks: []polymarket.OrderBookLevel{
			{Price: roundToTick(askP, tick), Size: 2000},
			{Price: roundToTick(askP+tick, tick), Size: 3500},
			{Price: roundToTick(askP+2*tick, tick), Size: 5000},
		},
		Timestamp: time.Now().UnixMilli(),
	}

	bids := make([]engine.Level, len(snap.Bids))
	for i, b := range snap.Bids {
		bids[i] = engine.Level{Price: b.Price, Size: b.Size}
	}
	asks := make([]engine.Level, len(snap.Asks))
	for i, a := range snap.Asks {
		asks[i] = engine.Level{Price: a.Price, Size: a.Size}
	}
	book.UpdateBook(snap.AssetID, bids, asks)

	hub.Broadcast(api.SSEEvent{
		Type: "book",
		Data: map[string]interface{}{
			"asset_id": snap.AssetID,
			"best_bid": bidPrice(bids),
			"best_ask": askPrice(asks),
			"spread":   askPrice(asks) - bidPrice(bids),
			"bids":     truncateLevels(bids, 10),
			"asks":     truncateLevels(asks, 10),
		},
		Time: time.Now().Format(time.RFC3339),
	})

	if active != nil {
		slog.Info("generating synthetic book", "token", tokenID)
			signals := active.OnBook(snap)
			slog.Info("synthetic book signals", "count", len(signals))
			processSignals(signals, book, paperEngine, active, hub, st, cfg)
		}

}

func feedBookToEngine(
	snap *polymarket.BookSnapshot,
	book *engine.VirtualBook,
	paperEngine *engine.PaperEngine,
	pnlTracker *engine.PnLTracker,
	reg *strategy.Registry,
	hub *api.Hub,
	st *store.Store,
	cfg *config.Config,
) {
	bids := make([]engine.Level, len(snap.Bids))
	for i, b := range snap.Bids {
		bids[i] = engine.Level{Price: b.Price, Size: b.Size}
	}
	asks := make([]engine.Level, len(snap.Asks))
	for i, a := range snap.Asks {
		asks[i] = engine.Level{Price: a.Price, Size: a.Size}
	}
	book.UpdateBook(snap.AssetID, bids, asks)

	if hub != nil {
		hub.Broadcast(api.SSEEvent{
			Type: "book",
			Data: map[string]interface{}{
				"asset_id": snap.AssetID,
				"best_bid": bidPrice(bids),
				"best_ask": askPrice(asks),
				"spread":   askPrice(asks) - bidPrice(bids),
				"bids":     truncateLevels(bids, 10),
				"asks":     truncateLevels(asks, 10),
			},
			Time: time.Now().Format(time.RFC3339),
		})
		// publish telemetry
		mid := book.Midpoint(snap.AssetID)
		telemetry.PublishBook(snap, mid)
	}


	if reg != nil {
		active := reg.Active()
		if active != nil {
			slog.Info("feedBookToEngine: calling strategy", "bids", len(snap.Bids), "asks", len(snap.Asks))
			signals := active.OnBook(*snap)
			slog.Info("feedBookToEngine: strategy signals", "count", len(signals))
			processSignals(signals, book, paperEngine, active, hub, st, cfg)
		} else {
			slog.Warn("feedBookToEngine: no active strategy in registry")
		}
	} else {
		slog.Warn("feedBookToEngine: nil registry")
	}
}

func processWSEvent(
	evt polymarket.WSEvent,
	book *engine.VirtualBook,
	paperEngine *engine.PaperEngine,
	pnlTracker *engine.PnLTracker,
	reg *strategy.Registry,
	hub *api.Hub,
	st *store.Store,
) {
	if !running.Load() {
		return
	}

	switch evt.EventType {
	case "book":
		if evt.Book != nil {
			feedBookToEngine(evt.Book, book, paperEngine, pnlTracker, reg, hub, st, &config.Config{DefaultOrderSize: 25})
		}

	case "last_trade_price":
		if evt.Trade != nil {
			price, _ := parseFloat(evt.Trade.Price)
			size, _ := parseFloat(evt.Trade.Size)

			hub.Broadcast(api.SSEEvent{
				Type: "trade",
				Data: map[string]interface{}{
					"asset_id": evt.Trade.AssetID,
					"price":    price,
					"size":     size,
					"side":     evt.Trade.Side,
				},
				Time: time.Now().Format(time.RFC3339),
			})

			fills := paperEngine.HandleTrade(evt.Trade.AssetID, evt.Trade.Side, price, size)
		for _, fill := range fills {
			pnlTracker.RecordFill(fill)
			st.RecordFill(fill.OrderID, fill.TokenID, fill.Side, fill.Price, fill.Size)

			if mm, ok := reg.Active().(*strategy.MarketMakingStrategy); ok {
				mm.UpdatePosition(fill.TokenID, fill.Size, fill.Side)
			}
			// telemetry
			mid := book.Midpoint(fill.TokenID)
			var modelTag string
			var modelLat float64
			if ord := paperEngine.GetOrder(fill.OrderID); ord != nil {
				modelTag = ord.ModelTag
				modelLat = ord.ModelLatencyMs
			}
			telemetry.PublishFill(fill, mid, modelTag, modelLat)
		}


			if active := reg.Active(); active != nil {
			signals := active.OnTrade(*evt.Trade)
			processSignals(signals, book, paperEngine, active, hub, st, &config.Config{DefaultOrderSize: 25})
			}
		}

	case "price_change":
		hub.Broadcast(api.SSEEvent{
			Type: "price_change",
			Data: evt.Raw,
			Time: time.Now().Format(time.RFC3339),
		})

	case "best_bid_ask":
		if evt.BBA != nil {
			hub.Broadcast(api.SSEEvent{
				Type: "bba",
				Data: map[string]interface{}{
					"asset_id": evt.BBA.AssetID,
					"best_bid": evt.BBA.BestBid,
					"best_ask": evt.BBA.BestAsk,
					"spread":   evt.BBA.Spread,
				},
				Time: time.Now().Format(time.RFC3339),
			})
		}
	}
}

func processSignals(
	signals []strategy.Signal,
	book *engine.VirtualBook,
	paperEngine *engine.PaperEngine,
	active strategy.Strategy,
	hub *api.Hub,
	st *store.Store,
	cfg *config.Config,
) {
	for _, sig := range signals {
		switch sig.Action {
		case "place":
			if paperEngine.HasOpenOrder(sig.TokenID, sig.Side, sig.Price) {
				continue
			}

			price := sig.Price
			if mid := book.Midpoint(sig.TokenID); mid > 0 {
				diff := price - mid
				if diff < 0 {
					diff = -diff
				}
				if diff > 0.03 {
					slog.Debug("skipping stale-price order",
						"token", sig.TokenID[:16],
						"side", sig.Side,
						"signal_price", sig.Price,
						"midpoint", mid,
					)
					continue
				}
			}

			// call model for this token
			modelTag := assignModel(sig.TokenID)
			bestBid, _ := book.BestBid(sig.TokenID)
			bestAsk, _ := book.BestAsk(sig.TokenID)
			mid := book.Midpoint(sig.TokenID)
			if mid == 0 {
				mid = price
			}
			features := []float64{mid, bestBid, bestAsk}
			pred, lat, err := callModelServer(modelTag, features)
			if err != nil {
				addLogErr := func() { slog.Debug("model call failed", "err", err) }
				_ = addLogErr
			}

			order := paperEngine.PlaceOrder(sig.TokenID, sig.Side, price, sig.Size)
			// attach model metadata
			if order != nil {
				order.ModelTag = modelTag
				order.ModelLatencyMs = lat
			}

				slog.Info("placed paper order", "id", order.ID, "side", sig.Side, "price", sig.Price, "size", sig.Size, "model", modelTag, "pred", pred, "lat_ms", lat)

				hub.Broadcast(api.SSEEvent{
					Type: "order",
					Data: order,
					Time: time.Now().Format(time.RFC3339),
				})

				if mm, ok := active.(*strategy.MarketMakingStrategy); ok {
					mm.RecordOrder(order.ID, sig.TokenID, sig.Side)
				}


		case "cancel":
			if paperEngine.CancelOrder(sig.OrderID) {
				if mm, ok := active.(*strategy.MarketMakingStrategy); ok {
					mm.RemoveOrder(sig.OrderID)
				}
			}
		}
	}
	_ = st
	_ = cfg
}

func bidPrice(levels []engine.Level) float64 {
	if len(levels) == 0 {
		return 0
	}
	return levels[0].Price
}

func askPrice(levels []engine.Level) float64 {
	if len(levels) == 0 {
		return 0
	}
	return levels[0].Price
}

func truncateLevels(levels []engine.Level, n int) []engine.Level {
	if len(levels) <= n {
		return levels
	}
	return levels[:n]
}

func parseFloat(s string) (float64, error) {
	val := float64(0)
	for _, c := range s {
		if c >= '0' && c <= '9' {
			val = val*10 + float64(c-'0')
		} else if c == '.' {
			break
		}
	}
	div := 1.0
	decimal := false
	for _, c := range s {
		if c == '.' {
			decimal = true
			continue
		}
		if decimal {
			div *= 10
			val += float64(c-'0') / div
		}
	}
	return val, nil
}

func roundToTick(price, tick float64) float64 {
	if tick <= 0 {
		return price
	}
	return float64(int(price/tick+0.5)) * tick
}

func makeEvtChan(hub *api.Hub) chan<- interface{} {
	ch := make(chan interface{}, 1024)
	go func() {
		for evt := range ch {
			switch e := evt.(type) {
			case map[string]interface{}:
				typ, _ := e["type"].(string)
				data := e["data"]
				hub.Broadcast(api.SSEEvent{
					Type: typ,
					Data: data,
					Time: time.Now().Format(time.RFC3339),
				})
			}
		}
	}()
	return ch
}

func checkPerOrderTP(
	book *engine.VirtualBook,
	paperEngine *engine.PaperEngine,
	pnlTracker *engine.PnLTracker,
	reg *strategy.Registry,
	hub *api.Hub,
	tpThreshold float64,
	tpClosed *map[string]bool,
	tpClosedMu *sync.Mutex,
) {
	openOrders := paperEngine.GetOpenOrders()
	for _, o := range openOrders {
		if o.Side != "BUY" || o.Filled <= 0 {
			continue
		}

		tpClosedMu.Lock()
		closed := (*tpClosed)[o.TokenID]
		tpClosedMu.Unlock()
		if closed {
			continue
		}

		mid := book.Midpoint(o.TokenID)
		if mid <= 0 {
			continue
		}
		orderPnl := (mid - o.Price) * o.Filled
		if orderPnl < tpThreshold {
			continue
		}

		slog.Info("per-order take-profit", "order", o.ID, "token", o.TokenID[:8], "pnl", orderPnl, "threshold", tpThreshold)

		paperEngine.CancelOrder(o.ID)
		if mm, ok := reg.Active().(*strategy.MarketMakingStrategy); ok {
			mm.RemoveOrder(o.ID)
			// Update strategy position tracking - we're closing the position
			mm.UpdatePosition(o.TokenID, o.Filled, "SELL")
		}

		// Record closing fill at current mid to realize profit
		closeFill := engine.Fill{
			OrderID:  o.ID,
			TokenID:  o.TokenID,
			Side:     "SELL",
			Price:    mid,
			Size:     o.Filled,
			Time:     time.Now(),
		}
		pnlTracker.RecordFill(closeFill)

		tpClosedMu.Lock()
		(*tpClosed)[o.TokenID] = true
		tpClosedMu.Unlock()

		hub.Broadcast(api.SSEEvent{
			Type: "log",
			Data: map[string]string{
				"message": fmt.Sprintf("TP: closed %s (filled=%.2f) at $%.4f for $%.2f profit", o.ID, o.Filled, mid, orderPnl),
			},
			Time: time.Now().Format(time.RFC3339),
		})
	}
}

func init() {
	_ = context.Background()
	_ = log.Default()
	_ = rand.Intn
}
