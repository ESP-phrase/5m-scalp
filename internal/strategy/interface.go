package strategy

import "scalp5/internal/polymarket"

type Signal struct {
	Action  string  `json:"action"`  // "place", "cancel"
	OrderID string  `json:"order_id,omitempty"`
	TokenID string  `json:"token_id,omitempty"`
	Side    string  `json:"side,omitempty"`
	Price   float64 `json:"price,omitempty"`
	Size    float64 `json:"size,omitempty"`
}

type Strategy interface {
	Name() string
	Description() string
	OnBook(snapshot polymarket.BookSnapshot) []Signal
	OnPriceChange(change polymarket.PriceChange) []Signal
	OnTrade(trade polymarket.LastTradePrice) []Signal
	OnTick() []Signal
	Reset()
}
