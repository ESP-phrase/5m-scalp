package engine

import (
	"sort"
	"sync"
)

type Level struct {
	Price float64
	Size  float64
}

type TokenBook struct {
	Bids []Level
	Asks []Level
}

type VirtualBook struct {
	mu     sync.RWMutex
	books  map[string]*TokenBook
}

func NewVirtualBook() *VirtualBook {
	return &VirtualBook{
		books: make(map[string]*TokenBook),
	}
}

func (vb *VirtualBook) UpdateBook(assetID string, bids, asks []Level) {
	vb.mu.Lock()
	defer vb.mu.Unlock()

	tb := &TokenBook{
		Bids: make([]Level, len(bids)),
		Asks: make([]Level, len(asks)),
	}
	copy(tb.Bids, bids)
	copy(tb.Asks, asks)

	sort.Slice(tb.Bids, func(i, j int) bool { return tb.Bids[i].Price > tb.Bids[j].Price })
	sort.Slice(tb.Asks, func(i, j int) bool { return tb.Asks[i].Price < tb.Asks[j].Price })

	vb.books[assetID] = tb
}

func (vb *VirtualBook) BestBid(assetID string) (float64, float64) {
	vb.mu.RLock()
	defer vb.mu.RUnlock()

	tb, ok := vb.books[assetID]
	if !ok || len(tb.Bids) == 0 {
		return 0, 0
	}
	return tb.Bids[0].Price, tb.Bids[0].Size
}

func (vb *VirtualBook) BestAsk(assetID string) (float64, float64) {
	vb.mu.RLock()
	defer vb.mu.RUnlock()

	tb, ok := vb.books[assetID]
	if !ok || len(tb.Asks) == 0 {
		return 0, 0
	}
	return tb.Asks[0].Price, tb.Asks[0].Size
}

func (vb *VirtualBook) Midpoint(assetID string) float64 {
	bid, _ := vb.BestBid(assetID)
	ask, _ := vb.BestAsk(assetID)
	if bid == 0 || ask == 0 {
		return 0
	}
	return (bid + ask) / 2
}

func (vb *VirtualBook) Spread(assetID string) float64 {
	bid, _ := vb.BestBid(assetID)
	ask, _ := vb.BestAsk(assetID)
	if bid == 0 || ask == 0 {
		return 0
	}
	return ask - bid
}

func (vb *VirtualBook) GetBook(assetID string) *TokenBook {
	vb.mu.RLock()
	defer vb.mu.RUnlock()
	return vb.books[assetID]
}

func (vb *VirtualBook) ClearToken(assetID string) {
	vb.mu.Lock()
	defer vb.mu.Unlock()
	delete(vb.books, assetID)
}

func (vb *VirtualBook) SizeAtPrice(assetID, side string, price float64) float64 {
	vb.mu.RLock()
	defer vb.mu.RUnlock()

	tb, ok := vb.books[assetID]
	if !ok {
		return 0
	}

	levels := tb.Bids
	if side == "SELL" {
		levels = tb.Asks
	}

	for _, l := range levels {
		if l.Price == price {
			return l.Size
		}
	}
	return 0
}
