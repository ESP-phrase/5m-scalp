package telemetry

import (
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"scalp5/internal/engine"
	"scalp5/internal/polymarket"

	_ "github.com/lib/pq"
	"github.com/go-redis/redis/v8"
)

var (
	rdb     *redis.Client
	db      *sql.DB
	csvPath string
	mu      sync.Mutex
	ctx     = context.Background()
)

func Init(redisAddr, neonDSN, csvOut string) error {
	if redisAddr != "" {
		rdb = redis.NewClient(&redis.Options{Addr: redisAddr})
		if err := rdb.Ping(ctx).Err(); err != nil {
			rdb = nil
		}
	}
	if neonDSN != "" {
		var err error
		db, err = sql.Open("postgres", neonDSN)
		if err != nil {
			db = nil
		} else if err = db.Ping(); err != nil {
			db = nil
		} else {
			initDB()
		}
	}
	csvPath = csvOut
	if csvPath != "" {
		f, err := os.OpenFile(csvPath, os.O_CREATE|os.O_RDWR, 0644)
		if err != nil {
			return err
		}
		defer f.Close()
		info, _ := f.Stat()
		if info.Size() == 0 {
			w := csv.NewWriter(f)
			w.Write([]string{"ts","type","token_id","mid","best_bid","best_ask","price","size","side","order_id","extra"})
			w.Flush()
		}
	}
	return nil
}

func initDB() {
	if db == nil { return }
	db.Exec(`CREATE TABLE IF NOT EXISTS telemetry (
		id SERIAL PRIMARY KEY,
		ts BIGINT NOT NULL DEFAULT 0,
		type TEXT NOT NULL,
		token_id TEXT NOT NULL DEFAULT '',
		mid DOUBLE PRECISION DEFAULT 0,
		best_bid DOUBLE PRECISION DEFAULT 0,
		best_ask DOUBLE PRECISION DEFAULT 0,
		price DOUBLE PRECISION DEFAULT 0,
		size DOUBLE PRECISION DEFAULT 0,
		side TEXT DEFAULT '',
		order_id TEXT DEFAULT '',
		model_tag TEXT DEFAULT '',
		latency_ms DOUBLE PRECISION DEFAULT 0,
		pred DOUBLE PRECISION DEFAULT 0,
		pnl DOUBLE PRECISION DEFAULT 0,
		reason TEXT DEFAULT '',
		created_at TIMESTAMPTZ DEFAULT NOW()
	);`)
	db.Exec(`CREATE INDEX IF NOT EXISTS idx_telemetry_ts ON telemetry(ts);`)
	db.Exec(`CREATE INDEX IF NOT EXISTS idx_telemetry_type ON telemetry(type);`)
	db.Exec(`CREATE INDEX IF NOT EXISTS idx_telemetry_token ON telemetry(token_id);`)
}

func insertDB(ts int64, typ, tokenID string, mid, bestBid, bestAsk, price, size float64, side, orderID, modelTag string, latencyMs, pred, pnl float64, reason string) {
	if db == nil { return }
	_, _ = db.Exec(`INSERT INTO telemetry
		(ts,type,token_id,mid,best_bid,best_ask,price,size,side,order_id,model_tag,latency_ms,pred,pnl,reason)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
		ts, typ, tokenID, mid, bestBid, bestAsk, price, size, side, orderID, modelTag, latencyMs, pred, pnl, reason)
}

func appendCSVRow(row []string) error {
	mu.Lock()
	defer mu.Unlock()
	if csvPath == "" { return nil }
	f, err := os.OpenFile(csvPath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil { return err }
	defer f.Close()
	w := csv.NewWriter(f)
	defer w.Flush()
	return w.Write(row)
}

func PublishBook(snap *polymarket.BookSnapshot, mid float64) {
	ts := time.Now().UnixMilli()
	row := []string{fmt.Sprintf("%d", ts), "book", snap.AssetID, fmt.Sprintf("%f", mid)}
	var bestBid, bestAsk float64
	if len(snap.Bids) > 0 { bestBid = snap.Bids[0].Price; row = append(row, fmt.Sprintf("%f", bestBid)) } else { row = append(row, "") }
	if len(snap.Asks) > 0 { bestAsk = snap.Asks[0].Price; row = append(row, fmt.Sprintf("%f", bestAsk)) } else { row = append(row, "") }
	row = append(row, "", "", "", "")
	_ = appendCSVRow(row)
	insertDB(ts, "book", snap.AssetID, mid, bestBid, bestAsk, 0, 0, "", "", "", 0, 0, 0, "")
}

func PublishFill(f engine.Fill, mid float64, modelTag string, latencyMs float64) {
	ts := time.Now().UnixMilli()
	row := []string{fmt.Sprintf("%d", ts), "fill", f.TokenID, fmt.Sprintf("%f", mid), "", "", fmt.Sprintf("%f", f.Price), fmt.Sprintf("%f", f.Size), f.Side, f.OrderID, modelTag}
	_ = appendCSVRow(row)
	insertDB(ts, "fill", f.TokenID, mid, 0, 0, f.Price, f.Size, f.Side, f.OrderID, modelTag, latencyMs, 0, 0, "")
	if rdb != nil {
		m := map[string]interface{}{"ts": ts, "type": "fill", "token_id": f.TokenID, "order_id": f.OrderID, "side": f.Side, "price": f.Price, "size": f.Size, "model_tag": modelTag, "latency": latencyMs}
		b, _ := json.Marshal(m)
		_ = rdb.XAdd(ctx, &redis.XAddArgs{Stream: "telemetry", Values: map[string]interface{}{"data": string(b)}}).Err()
	}
}

func PublishOrder(order *engine.PaperOrder, mid, pred float64, modelTag string, latencyMs float64) {
	if order == nil { return }
	ts := time.Now().UnixMilli()
	row := []string{fmt.Sprintf("%d", ts), "order", order.TokenID, fmt.Sprintf("%f", mid), "", "", fmt.Sprintf("%f", order.Price), fmt.Sprintf("%f", order.Size), order.Side, order.ID, fmt.Sprintf("%s|pred=%.6f|lat=%.3f", modelTag, pred, latencyMs)}
	_ = appendCSVRow(row)
	insertDB(ts, "order", order.TokenID, mid, 0, 0, order.Price, order.Size, order.Side, order.ID, modelTag, latencyMs, pred, 0, "")
}

func PublishClose(orderID, tokenID, side, reason string, price, filled, mid, pnl float64) {
	ts := time.Now().UnixMilli()
	row := []string{fmt.Sprintf("%d", ts), "close", tokenID, fmt.Sprintf("%f", mid), "", "", fmt.Sprintf("%f", price), fmt.Sprintf("%f", filled), side, orderID, fmt.Sprintf("%s|pnl=%.4f", reason, pnl)}
	_ = appendCSVRow(row)
	insertDB(ts, "close", tokenID, mid, 0, 0, price, filled, side, orderID, reason, 0, 0, pnl, reason)
}

func PublishLatency(typ string, ms float64) {
	ts := time.Now().UnixMilli()
	row := []string{fmt.Sprintf("%d", ts), "latency", "", "", "", "", "", fmt.Sprintf("%.3f", ms), "", "", typ}
	_ = appendCSVRow(row)
	insertDB(ts, "latency", "", 0, 0, 0, 0, ms, "", "", typ, ms, 0, 0, "")
}
