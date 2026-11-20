// Package config lê a configuração a partir de variáveis de ambiente.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Addr               string
	DatabaseURL        string
	DBMaxConns         int32
	JWTSecret          string
	JWTTTL             time.Duration
	AdminToken         string
	AlertsInterval     time.Duration
	StaleOfferAfter    time.Duration
	RateLimitPerSecond float64
	RateLimitBurst     int
	CORSOrigins        string
	TrustProxy         bool // usar X-Forwarded-For (só atrás de proxy confiável)
}

func Load() (Config, error) {
	c := Config{
		Addr:               env("ADDR", ":8080"),
		DatabaseURL:        env("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/comparai?sslmode=disable"),
		DBMaxConns:         int32(envInt("DB_MAX_CONNS", 20)),
		JWTSecret:          os.Getenv("JWT_SECRET"),
		JWTTTL:             envDuration("JWT_TTL", 24*time.Hour),
		AdminToken:         os.Getenv("ADMIN_TOKEN"),
		AlertsInterval:     envDuration("ALERTS_INTERVAL", time.Minute),
		StaleOfferAfter:    envDuration("STALE_OFFER_AFTER", 72*time.Hour),
		RateLimitPerSecond: envFloat("RATE_LIMIT_RPS", 20),
		RateLimitBurst:     envInt("RATE_LIMIT_BURST", 40),
		CORSOrigins:        env("CORS_ORIGINS", ""),
		TrustProxy:         os.Getenv("TRUST_PROXY") == "true",
	}
	if err := checkSecret("JWT_SECRET", c.JWTSecret, 32); err != nil {
		return c, err
	}
	if err := checkSecret("ADMIN_TOKEN", c.AdminToken, 16); err != nil {
		return c, err
	}
	return c, nil
}

// checkSecret recusa segredos vazios, curtos ou copiados do .env.example.
func checkSecret(name, v string, min int) error {
	switch {
	case v == "":
		return fmt.Errorf("%s é obrigatório", name)
	case len(v) < min:
		return fmt.Errorf("%s precisa ter pelo menos %d caracteres", name, min)
	case strings.HasPrefix(strings.ToLower(v), "troque") || v == "admin-dev":
		return fmt.Errorf("%s ainda está com valor de exemplo; gere um novo", name)
	}
	return nil
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(k)); err == nil {
		return v
	}
	return def
}

func envFloat(k string, def float64) float64 {
	if v, err := strconv.ParseFloat(os.Getenv(k), 64); err == nil {
		return v
	}
	return def
}

func envDuration(k string, def time.Duration) time.Duration {
	if v, err := time.ParseDuration(os.Getenv(k)); err == nil {
		return v
	}
	return def
}
