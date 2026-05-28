// latency.go — three-type latency tracker with pUSD erosion modeling.
package engine

import (
	"math"
	"sort"
	"sync"
	"time"
)

const GasPerFill = 0.02 // estimated Polygon CLOB gas per fill (pUSD)

var (
	totalGasCumulative float64
	gasMu              sync.Mutex
)

func AddGas(amount float64) {
	gasMu.Lock()
	totalGasCumulative += amount
	gasMu.Unlock()
}

func TotalGas() float64 {
	gasMu.Lock()
	defer gasMu.Unlock()
	return totalGasCumulative
}

type LatencySample struct {
	Type      string
	Ms        float64
	TokenID   string
	Timestamp time.Time
}

type LatencyTracker struct {
	mu       sync.RWMutex
	samples  []LatencySample
	maxSamps int
	lastBook time.Time
}

func NewLatencyTracker(maxSamples int) *LatencyTracker {
	if maxSamples <= 0 {
		maxSamples = 1000
	}
	return &LatencyTracker{
		samples:  make([]LatencySample, 0, maxSamples),
		maxSamps: maxSamples,
		lastBook: time.Now(),
	}
}

func (lt *LatencyTracker) Record(t LatencySample) {
	lt.mu.Lock()
	defer lt.mu.Unlock()
	lt.samples = append(lt.samples, t)
	if len(lt.samples) > lt.maxSamps {
		lt.samples = lt.samples[len(lt.samples)-lt.maxSamps:]
	}
}

func (lt *LatencyTracker) MarkBook() {
	lt.mu.Lock()
	defer lt.mu.Unlock()
	lt.lastBook = time.Now()
}

func (lt *LatencyTracker) SinceBook() time.Duration {
	lt.mu.RLock()
	defer lt.mu.RUnlock()
	if lt.lastBook.IsZero() {
		return 0
	}
	return time.Since(lt.lastBook)
}

// Stats returns avg/p50/p99 for a given type.
func (lt *LatencyTracker) Stats(typ string) (avg, p50, p99 float64, count int) {
	lt.mu.RLock()
	defer lt.mu.RUnlock()
	var vals []float64
	for _, s := range lt.samples {
		if s.Type == typ {
			vals = append(vals, s.Ms)
		}
	}
	if len(vals) == 0 {
		return 0, 0, 0, 0
	}
	sort.Float64s(vals)
	var sum float64
	for _, v := range vals {
		sum += v
	}
	count = len(vals)
	avg = sum / float64(count)
	idx50 := int(float64(count) * 0.50)
	idx99 := int(float64(count) * 0.99)
	if idx50 >= count {
		idx50 = count - 1
	}
	if idx99 >= count {
		idx99 = count - 1
	}
	p50 = vals[idx50]
	p99 = vals[idx99]
	return
}

// RealityScore returns pUSD erosion and reality percentage including gas fees.
func (lt *LatencyTracker) RealityScore(bookLatMs, e2eLatMs, closeLatMs, positionPnL, driftRate float64, fillCount int) (erosion, score float64) {
	if driftRate <= 0 {
		driftRate = 0.001
	}
	totalLatSec := (bookLatMs + e2eLatMs + closeLatMs) / 1000.0
	latErosion := math.Abs(driftRate * totalLatSec * math.Abs(positionPnL))
	gasCost := float64(fillCount) * GasPerFill
	erosion = latErosion + gasCost

	absPnL := math.Abs(positionPnL)
	if absPnL < 0.001 {
		erosion = gasCost
		return erosion, max(0, 100-erosion*100)
	}
	score = (1 - erosion/absPnL) * 100
	if score < 0 { score = 0 }
	if score > 100 { score = 100 }
	return
}
