package config

import (
	"bufio"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

func LoadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		slog.Debug("no .env file, using defaults", "path", path)
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])
		if os.Getenv(key) == "" {
			os.Setenv(key, val)
		}
	}
}

type Config struct {
	GammaURL             string
	ClobURL              string
	WsURL                string
	FillProbability      float64
	MarketFillProbability float64
	TickInterval         time.Duration
	MaxPositionPerToken  float64
	MarketSearchTags     []string
	APIPort              string
	DBPath               string
	DefaultOrderSize     float64
	MinSpread            float64
	MaxSpread            float64
	TakeProfit           float64
	StopLoss             float64
	FeeRate              float64
	Bankroll             float64
	RedisAddr            string
	DatabaseURL          string
	PositionAnalyzerURL  string
}

func Load() *Config {
	return &Config{
		GammaURL:             getEnv("POLYMARKET_GAMMA_URL", "https://gamma-api.polymarket.com"),
		ClobURL:              getEnv("POLYMARKET_CLOB_URL", "https://clob.polymarket.com"),
		WsURL:                getEnv("POLYMARKET_WS_URL", "wss://ws-subscriptions-clob.polymarket.com/ws/market"),
		FillProbability:      getEnvFloat("FILL_PROBABILITY", 0.85),
		MarketFillProbability: getEnvFloat("MARKET_FILL_PROBABILITY", 0.95),
		TickInterval:         time.Duration(getEnvInt("TICK_INTERVAL_MS", 500)) * time.Millisecond,
		MaxPositionPerToken:  getEnvFloat("MAX_POSITION_PER_TOKEN", 500),
			MarketSearchTags:     getEnvStringSlice("MARKET_SEARCH_TAGS", []string{"btc-updown-5m", "btc-up-or-down-5m", "btc", "bitcoin"}),
		APIPort:              getEnv("API_PORT", "8400"),
		DBPath:               getEnv("DB_PATH", "scalp5.db"),
		DefaultOrderSize:     getEnvFloat("DEFAULT_ORDER_SIZE", 25),
		MinSpread:            getEnvFloat("MIN_SPREAD", 0.01),
		MaxSpread:            getEnvFloat("MAX_SPREAD", 0.10),
		TakeProfit:           getEnvFloat("TAKE_PROFIT", 0),
		StopLoss:             getEnvFloat("STOP_LOSS", 0),
		FeeRate:              getEnvFloat("FEE_RATE", 0.001),
		Bankroll:             getEnvFloat("BANKROLL", 200),
		RedisAddr:            getEnv("REDIS_ADDR", "localhost:6379"),
		DatabaseURL:          getEnv("DATABASE_URL", ""),
		PositionAnalyzerURL:  getEnv("POSITION_ANALYZER_URL", "http://127.0.0.1:8003"),
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvFloat(key string, fallback float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return fallback
}

func getEnvStringSlice(key string, fallback []string) []string {
	if v := os.Getenv(key); v != "" {
		parts := strings.Split(v, ",")
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			if trimmed := strings.TrimSpace(p); trimmed != "" {
				out = append(out, trimmed)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return fallback
}
