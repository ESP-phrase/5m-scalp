package polymarket

import (
	"encoding/json"
	"time"
)

type OrderBookLevel struct {
	Price float64 `json:"price,string"`
	Size  float64 `json:"size"`
}

type BookSnapshot struct {
	EventType string           `json:"event_type"`
	AssetID   string           `json:"asset_id"`
	Market    string           `json:"market"`
	Bids      []OrderBookLevel `json:"bids"`
	Asks      []OrderBookLevel `json:"asks"`
	Timestamp int64            `json:"timestamp,string"`
	Hash      string           `json:"hash"`
}

type PriceChange struct {
	Market       string       `json:"market"`
	PriceChanges []PriceLevel `json:"price_changes"`
	Timestamp    int64        `json:"timestamp,string"`
	EventType    string       `json:"event_type"`
}

type PriceLevel struct {
	AssetID string `json:"asset_id"`
	Price   string `json:"price"`
	Size    string `json:"size"`
	Side    string `json:"side"`
	Hash    string `json:"hash"`
	BestBid string `json:"best_bid"`
	BestAsk string `json:"best_ask"`
}

type LastTradePrice struct {
	AssetID   string `json:"asset_id"`
	EventType string `json:"event_type"`
	Market    string `json:"market"`
	Price     string `json:"price"`
	Side      string `json:"side"`
	Size      string `json:"size"`
	Timestamp int64  `json:"timestamp,string"`
}

type BestBidAsk struct {
	EventType string `json:"event_type"`
	Market    string `json:"market"`
	AssetID   string `json:"asset_id"`
	BestBid   string `json:"best_bid"`
	BestAsk   string `json:"best_ask"`
	Spread    string `json:"spread"`
	Timestamp int64  `json:"timestamp,string"`
}

type WSEvent struct {
	EventType string
	AssetID   string
	Market    string
	Book      *BookSnapshot
	PriceChg  *PriceChange
	Trade     *LastTradePrice
	BBA       *BestBidAsk
	Raw       []byte
	Time      time.Time
}

type ClobBookResponse struct {
	AssetID string `json:"asset_id"`
	Market  string `json:"market"`
	Bids    []struct {
		Price string `json:"price"`
		Size  string `json:"size"`
	} `json:"bids"`
	Asks []struct {
		Price string `json:"price"`
		Size  string `json:"size"`
	} `json:"asks"`
	Hash string `json:"hash"`
}

type MidpointResponse struct {
	Midpoint string `json:"mid"`
}

type rawGammaMarket struct {
	ID                     string      `json:"id"`
	Question               string      `json:"question"`
	ConditionID            string      `json:"conditionId"`
	ClobTokenIDs           string      `json:"clobTokenIds"`
	Outcomes               string      `json:"outcomes"`
	Slug                   string      `json:"slug"`
	EnableOrderBook        bool        `json:"enableOrderBook"`
	Active                 bool        `json:"active"`
	Closed                 bool        `json:"closed"`
	Volume                 json.Number `json:"volume24hr"`
	VolumeNum              json.Number `json:"volume"`
	Liquidity              json.Number `json:"liquidity"`
	OrderPriceMinTickSize  json.Number `json:"orderPriceMinTickSize"`
	NegRisk                bool        `json:"negRisk"`
}

type GammaMarket struct {
	ID                 string
	Question           string
	ConditionID        string
	ClobTokenIDs       []string
	Outcomes           []string
	Slug               string
	EnableOrderBook    bool
	Active             bool
	Closed             bool
	Volume             float64
	Liquidity          float64
	TickSize           float64
	NegRisk            bool
}

func parseGammaMarkets(raw []byte) ([]GammaMarket, error) {
	var rawItems []rawGammaMarket
	if err := json.Unmarshal(raw, &rawItems); err != nil {
		return nil, err
	}

	markets := make([]GammaMarket, 0, len(rawItems))
	for _, item := range rawItems {
		m := GammaMarket{
			ID:              item.ID,
			Question:        item.Question,
			ConditionID:     item.ConditionID,
			Slug:            item.Slug,
			EnableOrderBook: item.EnableOrderBook,
			Active:          item.Active,
			Closed:          item.Closed,
			NegRisk:         item.NegRisk,
		}

		if v, err := item.Volume.Float64(); err == nil {
			m.Volume = v
		}
		if v, err := item.Liquidity.Float64(); err == nil {
			m.Liquidity = v
		}
		if v, err := item.OrderPriceMinTickSize.Float64(); err == nil {
			m.TickSize = v
		}

		if item.ClobTokenIDs != "" {
			var ids []string
			if err := json.Unmarshal([]byte(item.ClobTokenIDs), &ids); err == nil {
				m.ClobTokenIDs = ids
			}
		}

		if item.Outcomes != "" {
			var outcomes []string
			if err := json.Unmarshal([]byte(item.Outcomes), &outcomes); err == nil {
				m.Outcomes = outcomes
			}
		}

		markets = append(markets, m)
	}

	return markets, nil
}
