package engine

import (
	"sync"
	"time"
)

type Position struct {
	TokenID  string  `json:"token_id"`
	Size     float64 `json:"size"`
	AvgEntry float64 `json:"avg_entry"`
}

type PnLState struct {
	Realized   float64 `json:"realized"`
	Unrealized float64 `json:"unrealized"`
	Total      float64 `json:"total"`
	FillCount  int     `json:"fill_count"`
	FeeRate    float64 `json:"fee_rate"`
	Bankroll   float64 `json:"bankroll"`
}

type PnLTracker struct {
	mu         sync.RWMutex
	positions  map[string]*Position
	realized   float64
	fillCount  int
	fills      []Fill
	book       *VirtualBook
	feeRate    float64
	bankroll   float64
}

func NewPnLTracker(book *VirtualBook, feeRate, bankroll float64) *PnLTracker {
	return &PnLTracker{
		positions: make(map[string]*Position),
		book:      book,
		feeRate:   feeRate,
		bankroll:  bankroll,
	}
}

func (p *PnLTracker) RecordFill(fill Fill) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.fills = append(p.fills, fill)
	if len(p.fills) > 1000 {
		p.fills = p.fills[len(p.fills)-500:]
	}

	pos, ok := p.positions[fill.TokenID]
	if !ok {
		pos = &Position{TokenID: fill.TokenID}
		p.positions[fill.TokenID] = pos
	}

	if fill.Side == "BUY" {
		fee := fill.Size * fill.Price * p.feeRate
		totalCost := pos.Size*pos.AvgEntry + fill.Size*fill.Price + fee
		pos.Size += fill.Size
		if pos.Size > 0 {
			pos.AvgEntry = totalCost / pos.Size
		}
	} else {
		if pos.Size > 0 {
			closeSize := fill.Size
			if closeSize > pos.Size {
				closeSize = pos.Size
			}
			fee := closeSize * fill.Price * p.feeRate
			realized := closeSize * (fill.Price - pos.AvgEntry)
			p.realized += realized - fee
			pos.Size -= closeSize
			if pos.Size < 0.001 && pos.Size > -0.001 {
				pos.Size = 0
			}
		}
	}

	p.fillCount++
}

func (p *PnLTracker) GetState() PnLState {
	p.mu.RLock()
	defer p.mu.RUnlock()

	var unrealized float64
	for tokenID, pos := range p.positions {
		if pos.Size <= 0 {
			continue
		}
		mid := p.book.Midpoint(tokenID)
		if mid > 0 {
			unrealized += pos.Size * (mid - pos.AvgEntry)
		}
	}

	return PnLState{
		Realized:   round2(p.realized),
		Unrealized: round2(unrealized),
		Total:      round2(p.realized + unrealized),
		FillCount:  p.fillCount,
		FeeRate:    round2(p.feeRate * 100),
		Bankroll:   round2(p.bankroll),
	}
}

func (p *PnLTracker) GetPositions() []Position {
	p.mu.RLock()
	defer p.mu.RUnlock()

	var positions []Position
	for _, pos := range p.positions {
		if pos.Size > 0 {
			positions = append(positions, *pos)
		}
	}
	return positions
}

// ForceRealize converts all unrealized PnL to realized by closing every position at its current mid.
// Deducts fee_rate + gas_per_fill. Returns total crystallized PnL (net of fees and gas).
func (p *PnLTracker) ForceRealize(book *VirtualBook) (total, feeTotal, gasTotal float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var posCount int
	for tokenID, pos := range p.positions {
		if pos.Size <= 0 {
			continue
		}
		mid := book.Midpoint(tokenID)
		if mid <= 0 {
			continue
		}
		posCount++
		fee := pos.Size * mid * p.feeRate
		gas := GasPerFill
		pnl := pos.Size*(mid-pos.AvgEntry) - fee - gas
		p.realized += pnl
		total += pnl
		feeTotal += fee
		gasTotal += gas
		pos.Size = 0
	}
	return
}

func (p *PnLTracker) GetRecentFills(n int) []Fill {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if len(p.fills) <= n {
		out := make([]Fill, len(p.fills))
		copy(out, p.fills)
		return out
	}
	start := len(p.fills) - n
	out := make([]Fill, n)
	copy(out, p.fills[start:])
	return out
}

func (p *PnLTracker) GetFillsSince(t time.Time) []Fill {
	p.mu.RLock()
	defer p.mu.RUnlock()

	var out []Fill
	for i := len(p.fills) - 1; i >= 0; i-- {
		if p.fills[i].Time.After(t) {
			out = append(out, p.fills[i])
		} else {
			break
		}
	}

	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func round2(v float64) float64 {
	return float64(int(v*100+0.5)) / 100
}
