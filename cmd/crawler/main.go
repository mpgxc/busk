// Crawler: coleta páginas de produto das lojas (sitemap + JSON-LD schema.org) e envia para a ingestão.
//
//	crawler add-source -store SLUG -seed https://loja.example/sitemap.xml [-rps 0.5] [-refresh-min 360]
//	crawler run [-once]
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"comparai/internal/crawler"
	"comparai/internal/db"
	"comparai/internal/ingest"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "uso: crawler add-source|run [opções]")
		os.Exit(2)
	}
	if err := run(os.Args[1], os.Args[2:]); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(cmd string, args []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://postgres:postgres@localhost:5432/comparai?sslmode=disable"
	}
	pool, err := db.Connect(ctx, dbURL, 5)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool); err != nil {
		return err
	}

	switch cmd {
	case "add-source":
		return addSource(ctx, pool, args)
	case "run":
		return runWorker(ctx, pool, args)
	default:
		return fmt.Errorf("comando desconhecido: %s", cmd)
	}
}

// userAgent identifica o bot e um contato para o dono do site poder pedir para parar.
func userAgent() string {
	contact := os.Getenv("CRAWLER_CONTACT")
	if contact == "" {
		contact = "https://comparai.example/bot"
	}
	return "ComparaiBot/1.0 (+" + contact + ")"
}

func addSource(ctx context.Context, pool *pgxpool.Pool, args []string) error {
	fl := flag.NewFlagSet("add-source", flag.ExitOnError)
	storeSlug := fl.String("store", "", "slug da loja (já cadastrada via /admin/stores)")
	seed := fl.String("seed", "", "URL inicial: sitemap.xml ou página de listagem")
	rps := fl.Float64("rps", 0.5, "requisições por segundo ao host")
	refresh := fl.Int("refresh-min", 360, "intervalo de recoleta, em minutos (mínimo 15)")
	maxDepth := fl.Int("max-depth", 3, "profundidade de links seguidos a partir do seed")
	maxURLs := fl.Int("max-urls", 20000, "teto de URLs na fila desta fonte")
	include := fl.String("include", "", "regex: só segue links cujo caminho casa com ela")
	if err := fl.Parse(args); err != nil {
		return err
	}
	if *storeSlug == "" || *seed == "" {
		return fmt.Errorf("-store e -seed são obrigatórios")
	}
	u, err := url.Parse(*seed)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("-seed precisa ser uma URL http(s) absoluta")
	}

	var storeID int64
	if err := pool.QueryRow(ctx, `SELECT id FROM stores WHERE slug = $1`, *storeSlug).Scan(&storeID); err != nil {
		return fmt.Errorf("loja %q não encontrada: %w", *storeSlug, err)
	}
	src := crawler.Source{
		StoreID: storeID, Host: u.Host, RPS: *rps, RefreshMinutes: *refresh,
		MaxDepth: *maxDepth, MaxURLs: *maxURLs, IncludeRegex: *include,
	}
	if err := crawler.AddSource(ctx, pool, src, u.String()); err != nil {
		return err
	}
	slog.Info("fonte cadastrada", "store", *storeSlug, "host", u.Host, "seed", u.String())
	return nil
}

func runWorker(ctx context.Context, pool *pgxpool.Pool, args []string) error {
	fl := flag.NewFlagSet("run", flag.ExitOnError)
	once := fl.Bool("once", false, "processa o que está vencido e sai")
	if err := fl.Parse(args); err != nil {
		return err
	}
	w := crawler.NewWorker(pool, userAgent(), ingest.NewService(pool))
	if *once {
		n, err := w.Drain(ctx)
		slog.Info("crawler: concluído", "paginas", n)
		return err
	}
	return w.Run(ctx, 5*time.Second)
}
