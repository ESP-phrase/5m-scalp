package telemetry

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"scalp5/internal/engine"
	"scalp5/internal/polymarket"

	"github.com/go-redis/redis/v8"
)

var (
	rdb     *redis.Client
	csvPath string
	mu      sync.Mutex
	ctx     = context.Background()
)

func Init(redisAddr string, csvOut string) error {
	if redisAddr != "" {
		rdb = redis.NewClient(&redis.Options{Addr: redisAddr})
		// simple ping to verify
		if err := rdb.Ping(ctx).Err(); err != nil {
			// not fatal; keep rdb nil
			rdb = nil
			return err
		}
	}
	csvPath = csvOut
	// ensure file exists and has header
	if csvPath != "" {
		f, err := os.OpenFile(csvPath, os.O_CREATE|os.O_RDWR, 0644)
		if err != nil {
			return err
		}
		defer f.Close()
		// write header if empty
		info, _ := f.Stat()
		if info.Size() == 0 {
			w := csv.NewWriter(f)
			w.Write([]string{"ts","type","token_id","mid","best_bid","best_ask","price","size","side","order_id","extra"})
			w.Flush()
		}
	}
	return nil
}

func appendCSVRow(row []string) error {
	mu.Lock()
	defer mu.Unlock()
	if csvPath == "" {
		return nil
	}
	f, err := os.OpenFile(csvPath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	defer w.Flush()
	return w.Write(row)
}

func PublishBook(snap *polymarket.BookSnapshot, mid float64) {
	row := []string{fmt.Sprintf("%d", time.Now().UnixMilli()), "book", snap.AssetID, fmt.Sprintf("%f", mid)}
	if len(snap.Bids) > 0 {
		row = append(row, fmt.Sprintf("%f", snap.Bids[0].Price))
	} else {
		row = append(row, "")
	}
	if len(snap.Asks) > 0 {
		row = append(row, fmt.Sprintf("%f", snap.Asks[0].Price))
	} else {
		row = append(row, "")
	}
	row = append(row, "", "", "", "")
	_ = appendCSVRow(row)

	if rdb != nil {
		m := map[string]interface{}{
			"ts":       time.Now().UnixMilli(),
			"type":     "book",
			"token_id": snap.AssetID,
			"mid":      mid,
		}
		if len(snap.Bids) > 0 {
			m["best_bid"] = snap.Bids[0].Price
		}
		if len(snap.Asks) > 0 {
			m["best_ask"] = snap.Asks[0].Price
		}
		b, _ := json.Marshal(m)
		_ = rdb.XAdd(ctx, &redis.XAddArgs{Stream: "telemetry", Values: map[string]interface{}{"data": string(b)}}).Err()
	}
}

func PublishFill(f engine.Fill, mid float64, modelTag string, latencyMs float64) {
	row := []string{fmt.Sprintf("%d", time.Now().UnixMilli()), "fill", f.TokenID, fmt.Sprintf("%f", mid), "", "", fmt.Sprintf("%f", f.Price), fmt.Sprintf("%f", f.Size), f.Side, f.OrderID, modelTag}
	_ = appendCSVRow(row)
	if rdb != nil {
		m := map[string]interface{}{
			"ts":        time.Now().UnixMilli(),
			"type":      "fill",
			"token_id":  f.TokenID,
			"order_id":  f.OrderID,
			"side":      f.Side,
			"price":     f.Price,
			"size":      f.Size,
			"model_tag": modelTag,
			"latency":   latencyMs,
		}
		b, _ := json.Marshal(m)
		_ = rdb.XAdd(ctx, &redis.XAddArgs{Stream: "telemetry", Values: map[string]interface{}{"data": string(b)}}).Err()
	}
}
