package strategy

import (
	"log/slog"
	"sync"

	"scalp5/internal/polymarket"

	"github.com/gorilla/websocket"
)

// gorilla/websocket not needed here but keeping imports clean
var _ = websocket.CloseMessage

type MarketMakingStrategy struct {
	mu            sync.Mutex
	minSpread     float64
	maxSpread     float64
	orderSize     float64
	maxPosition   float64
	currentOrders map[string]string // tokenID -> side -> orderID
	sideOrders    map[string]map[string]string
	tickSize      float64
	position      map[string]float64
}

func NewMarketMaking(minSpread, maxSpread, orderSize, maxPosition float64) *MarketMakingStrategy {
	return &MarketMakingStrategy{
		minSpread:     minSpread,
		maxSpread:     maxSpread,
		orderSize:     orderSize,
		maxPosition:   maxPosition,
		currentOrders: make(map[string]string),
		sideOrders:    make(map[string]map[string]string),
		tickSize:      0.01,
		position:      make(map[string]float64),
	}
}

func (s *MarketMakingStrategy) Name() string {
	return "market_making"
}

func (s *MarketMakingStrategy) Description() string {
	return "Place bid and ask limit orders to capture the spread"
}

func (s *MarketMakingStrategy) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.currentOrders = make(map[string]string)
	s.sideOrders = make(map[string]map[string]string)
}

func (s *MarketMakingStrategy) OnBook(snapshot polymarket.BookSnapshot) []Signal {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(snapshot.Bids) == 0 || len(snapshot.Asks) == 0 {
		return nil
	}

	bestBid := snapshot.Bids[0].Price
	bestAsk := snapshot.Asks[0].Price
	spread := bestAsk - bestBid

	if spread < s.minSpread || spread > s.maxSpread {
		return nil
	}

	// Don't quote if we're at max position
	pos := s.position[snapshot.AssetID]
	if pos >= s.maxPosition {
		return nil
	}

	var signals []Signal

	// Cancel existing orders for this token
	tokenKey := "token_" + snapshot.AssetID
	if sides, ok := s.sideOrders[snapshot.AssetID]; ok {
		for _, orderID := range sides {
			signals = append(signals, Signal{
				Action:  "cancel",
				OrderID: orderID,
			})
		}
		delete(s.sideOrders, snapshot.AssetID)
	}
	_ = tokenKey

	tick := s.tickSize
	bidPrice := roundToTick(bestBid+tick, tick)
	askPrice := roundToTick(bestAsk-tick, tick)

	if askPrice <= bidPrice {
		// spread too tight for tick, use midpoint
		mid := (bestBid + bestAsk) / 2
		bidPrice = roundToTick(mid-tick, tick)
		askPrice = roundToTick(mid+tick, tick)
	}

	signals = append(signals, Signal{
		Action:  "place",
		TokenID: snapshot.AssetID,
		Side:    "BUY",
		Price:   bidPrice,
		Size:    s.orderSize,
	})

	signals = append(signals, Signal{
		Action:  "place",
		TokenID: snapshot.AssetID,
		Side:    "SELL",
		Price:   askPrice,
		Size:    s.orderSize,
	})

	slog.Debug("market making quoting",
		"asset", snapshot.AssetID,
		"bid", bidPrice,
		"ask", askPrice,
		"spread", spread,
	)

	return signals
}

func (s *MarketMakingStrategy) OnPriceChange(change polymarket.PriceChange) []Signal {
	return nil
}

func (s *MarketMakingStrategy) OnTrade(trade polymarket.LastTradePrice) []Signal {
	// Let new book snapshots drive re-quoting
	return nil
}

func (s *MarketMakingStrategy) OnTick() []Signal {
	return nil
}

func (s *MarketMakingStrategy) RecordOrder(orderID, tokenID, side string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.sideOrders[tokenID] == nil {
		s.sideOrders[tokenID] = make(map[string]string)
	}
	s.sideOrders[tokenID][side] = orderID
}

func (s *MarketMakingStrategy) RemoveOrder(orderID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for tokenID, sides := range s.sideOrders {
		for side, id := range sides {
			if id == orderID {
				delete(sides, side)
				if len(sides) == 0 {
					delete(s.sideOrders, tokenID)
				}
				return
			}
		}
	}
}

func (s *MarketMakingStrategy) RemoveAllOrders() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sideOrders = make(map[string]map[string]string)
}

func (s *MarketMakingStrategy) UpdatePosition(tokenID string, fillSize float64, side string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if side == "BUY" {
		s.position[tokenID] += fillSize
	} else {
		s.position[tokenID] -= fillSize
		if s.position[tokenID] < 0 {
			s.position[tokenID] = 0
		}
	}
}

func roundToTick(price, tick float64) float64 {
	if tick <= 0 {
		return price
	}
	rounded := float64(int(price/tick+0.5)) * tick
	return float64(int(rounded*10000)) / 10000
}
