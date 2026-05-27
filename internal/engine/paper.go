package engine

import (
	"math/rand"
	"sync"
	"sync/atomic"
	"time"
)

type PaperOrder struct {
	ID             string    `json:"id"`
	TokenID        string    `json:"token_id"`
	Side           string    `json:"side"`
	Price          float64   `json:"price"`
	Size           float64   `json:"size"`
	Filled         float64   `json:"filled"`
	Status         string    `json:"status"`
	CreatedAt      time.Time `json:"created_at"`
	VisibleAt      time.Time `json:"visible_at"`
	ExpiresAt      time.Time `json:"expires_at"`
	ModelTag       string    `json:"model_tag,omitempty"`
	ModelLatencyMs float64   `json:"model_latency_ms,omitempty"`
	EntryMid       float64   `json:"entry_mid"`
	FilledValue    float64   `json:"filled_value"`
}

type Fill struct {
	OrderID  string    `json:"order_id"`
	TokenID  string    `json:"token_id"`
	Side     string    `json:"side"`
	Price    float64   `json:"price"`
	Size     float64   `json:"size"`
	Time     time.Time `json:"time"`
}

type PaperEngine struct {
	mu          sync.RWMutex
	orders      map[string]*PaperOrder
	book        *VirtualBook
	fillProb    float64
	marketFillProb float64
	nextID      int64
	eventCh     chan<- interface{}
}

func NewPaperEngine(book *VirtualBook, fillProb, marketFillProb float64) *PaperEngine {
	return &PaperEngine{
		orders:      make(map[string]*PaperOrder),
		book:        book,
		fillProb:    fillProb,
		marketFillProb: marketFillProb,
	}
}

func (pe *PaperEngine) SetEventChannel(ch chan<- interface{}) {
	pe.eventCh = ch
}

func (pe *PaperEngine) PlaceOrder(tokenID, side string, price, size float64) *PaperOrder {
	id := atomic.AddInt64(&pe.nextID, 1)

	now := time.Now()
	order := &PaperOrder{
		ID:        formatID(id),
		TokenID:   tokenID,
		Side:      side,
		Price:     price,
		Size:      size,
		Status:    "open",
		CreatedAt: now,
		VisibleAt: now, // may be adjusted by SetOrderModelMeta
		ExpiresAt: now.Add(60 * time.Second),
	}

	pe.mu.Lock()
	pe.orders[order.ID] = order
	pe.mu.Unlock()

	if pe.eventCh != nil {
		pe.eventCh <- map[string]interface{}{
			"type": "order",
			"data": order,
		}
	}

	return order
}

func (pe *PaperEngine) FillOrder(orderID string, size float64) *Fill {
	pe.mu.Lock()
	defer pe.mu.Unlock()

	order, ok := pe.orders[orderID]
	if !ok {
		return nil
	}
	if order.Status != "open" && order.Status != "partial" {
		return nil
	}

	// respect visibility delay
	if time.Now().Before(order.VisibleAt) {
		return nil
	}

	if rand.Float64() > pe.fillProb {
		return nil
	}

	orderRemaining := order.Size - order.Filled
	if size > orderRemaining {
		size = orderRemaining
	}
	size = clampSize(size, 0.01)
	if size == 0 {
		return nil
	}

	order.Filled += size
	order.FilledValue += size * order.Price
	if order.Filled >= order.Size-0.001 {
		order.Filled = order.Size
		order.Status = "filled"
	} else {
		order.Status = "partial"
	}

	fill := Fill{
		OrderID: order.ID,
		TokenID: order.TokenID,
		Side:    order.Side,
		Price:   order.Price,
		Size:    size,
		Time:    time.Now(),
	}

	if pe.eventCh != nil {
		pe.eventCh <- map[string]interface{}{
			"type": "fill",
			"data": fill,
		}
	}

	return &fill
}
func (pe *PaperEngine) CancelOrder(orderID string) bool {
	pe.mu.Lock()
	defer pe.mu.Unlock()

	order, ok := pe.orders[orderID]
	if !ok {
		return false
	}

	if order.Status == "open" || order.Status == "partial" {
		order.Status = "cancelled"

		if pe.eventCh != nil {
			pe.eventCh <- map[string]interface{}{
				"type": "order",
				"data": order,
			}
		}
		return true
	}

	return false
}

func (pe *PaperEngine) CancelAllForToken(tokenID string) []string {
	pe.mu.Lock()
	defer pe.mu.Unlock()

	var cancelled []string
	for _, order := range pe.orders {
		if order.TokenID == tokenID && (order.Status == "open" || order.Status == "partial") {
			order.Status = "cancelled"
			cancelled = append(cancelled, order.ID)

			if pe.eventCh != nil {
				pe.eventCh <- map[string]interface{}{
					"type": "order",
					"data": order,
				}
			}
		}
	}
	return cancelled
}

func (pe *PaperEngine) CancelAllOrders() int {
	pe.mu.Lock()
	defer pe.mu.Unlock()

	count := 0
	for _, order := range pe.orders {
		if order.Status == "open" || order.Status == "partial" {
			order.Status = "cancelled"
			count++

			if pe.eventCh != nil {
				pe.eventCh <- map[string]interface{}{
					"type": "order",
					"data": order,
				}
			}
		}
	}
	return count
}

func (pe *PaperEngine) SetOrderModelMeta(orderID, modelTag string, latencyMs float64) {
	pe.mu.Lock()
	defer pe.mu.Unlock()
	if order, ok := pe.orders[orderID]; ok {
		order.ModelTag = modelTag
		order.ModelLatencyMs = latencyMs
		order.VisibleAt = order.CreatedAt.Add(time.Duration(latencyMs) * time.Millisecond)
	}
}


func (pe *PaperEngine) HandleTrade(assetID, side string, price, size float64) []Fill {
	pe.mu.Lock()
	defer pe.mu.Unlock()

	var fills []Fill

	// Determine which side of our orders can be hit:
	// If taker side = SELL (someone sold at bid), their trade matched BUY orders on the book.
	// Our BUY orders at that price could fill.
	// If taker side = BUY (someone bought at ask), our SELL orders could fill.
	ourSide := "BUY"
	if side == "BUY" {
		ourSide = "SELL"
	}

	// Collect all our open/partial orders at this price and side
	var candidates []*PaperOrder
	for _, order := range pe.orders {
		if order.TokenID == assetID &&
			order.Side == ourSide &&
			order.Price == price &&
			(order.Status == "open" || order.Status == "partial") {
			candidates = append(candidates, order)
		}
	}

	if len(candidates) == 0 {
		return nil
	}

	// Total paper size at this level
	var paperTotal float64
	for _, o := range candidates {
		paperTotal += o.Size - o.Filled
	}

	bookSize := pe.book.SizeAtPrice(assetID, ourSide, price)
	totalAtLevel := bookSize + paperTotal

	// Distribute fills among candidates
	remaining := size
for _, order := range candidates {
			// skip not yet visible orders
			if time.Now().Before(order.VisibleAt) {
				continue
			}
			orderRemaining := order.Size - order.Filled
			// Proportional fill
			share := orderRemaining / totalAtLevel
			fillSize := remaining * share
			
			// dynamic slippage: if order exceeds book size, reduce fill
			if orderRemaining > bookSize && bookSize > 0 {
				reduction := bookSize / (bookSize + orderRemaining)
				fillSize = fillSize * reduction
			}
			
			// Apply fill probability adjusted by depth
			prob := pe.fillProb
			if bookSize < 1 {
				prob *= 0.5
			}
			if rand.Float64() > prob {
				continue
			}
			
			if fillSize > orderRemaining {
				fillSize = orderRemaining
			}
			
			fillSize = clampSize(fillSize, 0.01)
			
			order.Filled += fillSize
			remaining -= fillSize
			
			if order.Filled >= order.Size-0.001 {
				order.Filled = order.Size
				order.Status = "filled"
			} else {
				order.Status = "partial"
			}
			
			fill := Fill{
				OrderID: order.ID,
				TokenID: order.TokenID,
				Side:    order.Side,
				Price:   price,
				Size:    fillSize,
				Time:    time.Now(),
			}
			fills = append(fills, fill)
			
			if pe.eventCh != nil {
				pe.eventCh <- map[string]interface{}{
					"type": "fill",
					"data": fill,
				}
			}
		}


	
	// cancel expired orders that remain open
	for _, o := range pe.orders {
		if (o.Status == "open" || o.Status == "partial") && time.Now().After(o.ExpiresAt) {
			o.Status = "cancelled"
			if pe.eventCh != nil {
				pe.eventCh <- map[string]interface{}{"type": "order", "data": o}
			}
		}
	}

	return fills
}


func (pe *PaperEngine) GetOrders() []*PaperOrder {
	pe.mu.RLock()
	defer pe.mu.RUnlock()

	orders := make([]*PaperOrder, 0, len(pe.orders))
	for _, o := range pe.orders {
		orders = append(orders, o)
	}
	return orders
}

func (pe *PaperEngine) GetOrder(orderID string) *PaperOrder {
	pe.mu.RLock()
	defer pe.mu.RUnlock()
	return pe.orders[orderID]
}

func (pe *PaperEngine) GetOpenOrders() []*PaperOrder {
	pe.mu.RLock()
	defer pe.mu.RUnlock()

	var orders []*PaperOrder
	for _, o := range pe.orders {
		if o.Status == "open" || o.Status == "partial" {
			orders = append(orders, o)
		}
	}
	return orders
}

func (pe *PaperEngine) HasOpenOrder(tokenID, side string, price float64) bool {
	pe.mu.RLock()
	defer pe.mu.RUnlock()

	for _, o := range pe.orders {
		if o.TokenID == tokenID && o.Side == side && o.Price == price &&
			(o.Status == "open" || o.Status == "partial") {
			return true
		}
	}
	return false
}

func formatID(id int64) string {
	letters := "abcdefghijklmnopqrstuvwxyz"
	n := id
	result := make([]byte, 0)
	for n > 0 {
		n--
		result = append([]byte{letters[n%26]}, result...)
		n /= 26
	}
	if len(result) == 0 {
		return "a"
	}
	return string(result)
}

func clampSize(s, min float64) float64 {
	if s < min {
		return 0
	}
	return s
}
