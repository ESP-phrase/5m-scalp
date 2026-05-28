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
var latTracker *engine.LatencyTracker
var (
	modelAssign = make(map[string]string)
	modelMu     sync.Mutex
	rrCounter   int
)

var (
	trackedMu  sync.Mutex
	trackedIDs []string
)

func addTrackedTokens(ids []string) {
	trackedMu.Lock()
	defer trackedMu.Unlock()
	for _, id := range ids {
		found := false
		for _, t := range trackedIDs {
			if t == id {
				found = true
				break
			}
		}
		if !found {
			trackedIDs = append(trackedIDs, id)
		}
	}
}

func getTrackedTokens() []string {
	trackedMu.Lock()
	defer trackedMu.Unlock()
	cp := make([]string, len(trackedIDs))
	copy(cp, trackedIDs)
	return cp
}

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

// safeGo runs fn in a goroutine with panic recovery logging.
func safeGo(fn func(), name string) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("goroutine panicked", "name", name, "recover", r)
			}
		}()
		fn()
	}()
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

	var seenTokenIDs = make(map[string]bool)
	for _, m := range markets {
		for _, tid := range m.ClobTokenIDs {
			if !seenTokenIDs[tid] {
				seenTokenIDs[tid] = true
				trackedIDs = append(trackedIDs, tid)
			}
		}
	}
	tokenIDs := trackedIDs

	slog.Info("subscribing to tokens", "count", len(tokenIDs))

	book := engine.NewVirtualBook()
	paperEngine := engine.NewPaperEngine(book, cfg.FillProbability, cfg.MarketFillProbability)
	pnlTracker := engine.NewPnLTracker(book, cfg.FeeRate, cfg.Bankroll)
	latTracker = engine.NewLatencyTracker(1000)

	reg := strategy.NewRegistry()
	mm := strategy.NewMarketMaking(cfg.MinSpread, cfg.MaxSpread, cfg.DefaultOrderSize, cfg.MaxPositionPerToken)
	reg.Register(mm)
	reg.SetActive("market_making")

	hub := api.NewHub()
	paperEngine.SetEventChannel(makeEvtChan(hub))

	server := api.NewServer(hub, st, paperEngine, pnlTracker, reg, book, latTracker)
	server.SetMarkets(markets)
	if err := telemetry.Init(cfg.RedisAddr, cfg.DatabaseURL, "telemetry.csv"); err != nil {
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
		safeGo(func() {
			for {
				if err := wsClient.Connect(); err != nil {
					slog.Warn("ws connect failed", "err", err)
					time.Sleep(3 * time.Second)
					continue
				}
				for evt := range wsClient.Events() {
					select {
					case wsEvents <- evt:
					default:
					}
				}
				slog.Warn("ws events closed, backing off before reconnect", "backoff", "3s")
				time.Sleep(3 * time.Second)
			}
		}, "wsLoop")
	}

	// Seed initial book data via REST
	safeGo(func() {
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
	}, "bookSeeding")

	marketsRefresh := time.NewTicker(2 * time.Minute)
	defer marketsRefresh.Stop()

	safeGo(func() {
		for range marketsRefresh.C {
			newMarkets, err := client.DiscoverMarkets(cfg.MarketSearchTags)
			if err != nil {
				continue
			}
			var newTokens []string
			for _, m := range newMarkets {
				for _, tid := range m.ClobTokenIDs {
					if !seenTokenIDs[tid] {
						seenTokenIDs[tid] = true
						newTokens = append(newTokens, tid)
					}
				}
			}
			if len(newTokens) > 0 {
				slog.Info("new markets found", "count", len(newTokens))
				server.SetMarkets(newMarkets)
				addTrackedTokens(newTokens)

				// Subscribe WS to new tokens
				newWS := polymarket.NewWSClient(cfg.WsURL, newTokens)
				safeGo(func() {
					for {
						if err := newWS.Connect(); err != nil {
							slog.Warn("ws connect failed (new tokens)", "err", err)
							time.Sleep(3 * time.Second)
							continue
						}
						for evt := range newWS.Events() {
							select {
							case wsEvents <- evt:
							default:
							}
						}
						slog.Warn("ws events closed (new tokens), reconnecting", "backoff", "3s")
						time.Sleep(3 * time.Second)
					}
				}, "wsLoopNewTokens")

				// Seed book data for new tokens
				safeGo(func() {
					time.Sleep(2 * time.Second)
					for _, tid := range newTokens {
						snap, err := client.GetBook(tid)
						if err != nil {
							genSyntheticBook(tid, book, reg.Active(), paperEngine, hub, st, cfg)
							continue
						}
						if len(snap.Bids) > 0 || len(snap.Asks) > 0 {
							feedBookToEngine(snap, book, paperEngine, pnlTracker, reg, hub, st, cfg)
						} else {
							genSyntheticBook(tid, book, reg.Active(), paperEngine, hub, st, cfg)
						}
					}
				}, "seedNewTokens")
			}
		}
	}, "marketsRefresh")

	safeGo(func() { runEngine(wsEvents, book, paperEngine, pnlTracker, reg, hub, st, cfg, client, tokenIDs, controlCh, func() {
		server.Stop()
	}) }, "runEngine")

	safeGo(func() {
		pnlTicker := time.NewTicker(10 * time.Second)
		defer pnlTicker.Stop()
		for range pnlTicker.C {
			state := pnlTracker.GetState()
			st.RecordPnL(state.Realized, state.Unrealized)
		}
	}, "pnlRecorder")

	// High-frequency per-order take-profit / stop-loss check (100ms)
	if cfg.TakeProfit > 0 || cfg.StopLoss > 0 {
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
							slog.Error("TP/SL check panicked", "recover", r)
						}
					}()
					checkOrderBounds(book, paperEngine, pnlTracker, reg, hub, cfg.TakeProfit, cfg.StopLoss)
				}()
			}
		}()
	}

	safeGo(func() {
		for {
			slog.Info("api server starting", "port", cfg.APIPort)
			if err := http.ListenAndServe(":"+cfg.APIPort, server.Handler()); err != nil {
				slog.Error("server error, restarting in 2s", "err", err)
				time.Sleep(2 * time.Second)
			}
		}
	}, "httpServer")

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
	defer func() {
		if r := recover(); r != nil {
			slog.Error("runEngine panicked", "recover", r)
		}
	}()
	ticker := time.NewTicker(cfg.TickInterval)
	defer ticker.Stop()

	bookRefresh := time.NewTicker(1 * time.Second)
	defer bookRefresh.Stop()

	lastBookTime := make(map[string]time.Time)
	lastFillCount := 0
	stallTicks := 0

	for {
		select {
		case start := <-controlCh:
			if start {
				slog.Info("engine started via control")
				go seedBookData(getTrackedTokens(), client, book, paperEngine, pnlTracker, reg, hub, st, cfg)
			} else {
				slog.Info("engine stopped via control — cancelling all orders")
				for _, tid := range getTrackedTokens() {
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
			if !evt.Book.ServerTime.IsZero() && evt.Book.ServerTime.Before(time.Now()) {
				latTracker.Record(engine.LatencySample{
					Type: "ws", Ms: float64(time.Since(evt.Book.ServerTime).Milliseconds()),
					TokenID: evt.Book.AssetID, Timestamp: time.Now(),
				})
			}
			latTracker.MarkBook()
			lastBookTime[evt.Book.AssetID] = time.Now()
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

			// Purge expired orders so synthetic books can generate new ones
			for _, o := range paperEngine.GetOpenOrders() {
				if time.Now().After(o.ExpiresAt) {
					paperEngine.CancelOrder(o.ID)
					if mm, ok := active.(*strategy.MarketMakingStrategy); ok {
						mm.RemoveOrder(o.ID)
					}
				}
			}

			// Stall detection: if no fills for 60s, force-reset
			fc := pnlTracker.GetState().FillCount
			if fc != lastFillCount {
				lastFillCount = fc
				stallTicks = 0
			} else {
				stallTicks++
			}
			if stallTicks > 120 {
				slog.Warn("stall detected — force resetting engine", "stall_ticks", stallTicks)
				paperEngine.CancelAllOrders()
				for _, tid := range getTrackedTokens() {
					book.ClearToken(tid)
				}
				active.Reset()
				go seedBookData(getTrackedTokens(), client, book, paperEngine, pnlTracker, reg, hub, st, cfg)
				stallTicks = 0
			}

			signals := active.OnTick()
			processSignals(signals, book, paperEngine, active, hub, st, cfg)

			// Generate synthetic books for tokens with no orders
			for _, tid := range getTrackedTokens() {
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
				genSyntheticBook(tid, book, active, paperEngine, hub, st, cfg)
				lastBookTime[tid] = time.Now()
			}

			// Synthetic trade generation for P&L visibility
			for _, tid := range getTrackedTokens() {
				if rand.Float64() > 0.15 {
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
					engine.AddGas(engine.GasPerFill)
					st.RecordFill(fill.OrderID, fill.TokenID, fill.Side, fill.Price, fill.Size)
					if mm, ok := active.(*strategy.MarketMakingStrategy); ok {
						mm.UpdatePosition(fill.TokenID, fill.Size, fill.Side)
					}
				}
			}

			// Check per-order take-profit / stop-loss
			if cfg.TakeProfit > 0 || cfg.StopLoss > 0 {
				checkOrderBounds(book, paperEngine, pnlTracker, reg, hub, cfg.TakeProfit, cfg.StopLoss)
			}

		case <-bookRefresh.C:
			if !running.Load() {
				continue
			}
			// Poll REST for book data as fallback
			for _, tid := range getTrackedTokens() {
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
	if latTracker != nil {
		latTracker.MarkBook()
	}

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

	if latTracker != nil {
		latTracker.MarkBook()
	}

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
			engine.AddGas(engine.GasPerFill)
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
			if order == nil {
				continue
			}
			order.EntryMid = mid
			paperEngine.SetOrderModelMeta(order.ID, modelTag, lat)
			slog.Info("placed paper order", "id", order.ID, "side", sig.Side, "price", sig.Price, "size", sig.Size, "model", modelTag, "pred", pred, "lat_ms", lat)
			telemetry.PublishOrder(order, mid, pred, modelTag, lat)
			if latTracker != nil {
				latTracker.Record(engine.LatencySample{
					Type: "e2e", Ms: float64(latTracker.SinceBook().Milliseconds()),
					TokenID: order.TokenID, Timestamp: time.Now(),
				})
			}

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

func checkOrderBounds(
	book *engine.VirtualBook,
	paperEngine *engine.PaperEngine,
	pnlTracker *engine.PnLTracker,
	reg *strategy.Registry,
	hub *api.Hub,
	tpThreshold float64,
	slThreshold float64,
) {
	openOrders := paperEngine.GetOpenOrders()
	for _, o := range openOrders {
		if o.Filled <= 0 {
			continue
		}

		mid := o.EntryMid
		if mid <= 0 {
			mid = o.Price
		}

		var orderPnl float64
		if o.Side == "BUY" {
			orderPnl = (mid - o.Price) * o.Filled
		} else {
			orderPnl = (o.Price - mid) * o.Filled
		}

		var reason string
		if tpThreshold > 0 && orderPnl >= tpThreshold {
			reason = "TP"
		} else if slThreshold > 0 && -orderPnl >= slThreshold {
			reason = "SL"
		} else {
			continue
		}

		slog.Info("per-order close", "order", o.ID, "token", o.TokenID[:8], "reason", reason, "pnl", orderPnl)

		paperEngine.CancelOrder(o.ID)
		if mm, ok := reg.Active().(*strategy.MarketMakingStrategy); ok {
			mm.RemoveOrder(o.ID)
			closeSide := "SELL"
			if o.Side == "SELL" {
				closeSide = "BUY"
			}
			mm.UpdatePosition(o.TokenID, o.Filled, closeSide)
		}

		closeFill := engine.Fill{
			OrderID:  o.ID,
			TokenID:  o.TokenID,
			Side:     "SELL",
			Price:    mid,
			Size:     o.Filled,
			Time:     time.Now(),
		}
		if o.Side == "SELL" {
			closeFill.Side = "BUY"
		}
		pnlTracker.RecordFill(closeFill)
		engine.AddGas(engine.GasPerFill)
		telemetry.PublishClose(o.ID, o.TokenID, o.Side, reason, mid, o.Filled, mid, orderPnl)
		if latTracker != nil {
			latTracker.Record(engine.LatencySample{
				Type: "close", Ms: float64(latTracker.SinceBook().Milliseconds()),
				TokenID: o.TokenID, Timestamp: time.Now(),
			})
		}

		hub.Broadcast(api.SSEEvent{
			Type: "log",
			Data: map[string]string{
				"message": fmt.Sprintf("%s: closed %s (%s filled=%.2f) at $%.4f for $%.2f %s",
					reason, o.ID, o.Side, o.Filled, mid, orderPnl, reason),
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
