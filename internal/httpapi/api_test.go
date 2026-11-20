package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"comparai/internal/alerts"
	"comparai/internal/auth"
	"comparai/internal/catalog"
	"comparai/internal/config"
	"comparai/internal/db"
	"comparai/internal/httpapi"
	"comparai/internal/ingest"
)

// Teste de integração: precisa de um Postgres descartável.
//
//	TEST_DATABASE_URL=postgres://postgres@localhost:5432/comparai_test?sslmode=disable go test ./...
//
// O schema public do banco de teste é APAGADO a cada execução.
func setup(t *testing.T) (*httptest.Server, *alerts.Service) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL não definido; pulando teste de integração")
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	pool, err := db.Connect(ctx, url, 5)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public;`); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}

	cfg := config.Config{
		JWTSecret: "test-secret-test-secret-test-secret", JWTTTL: time.Hour, AdminToken: "admin",
		RateLimitPerSecond: 1000, RateLimitBurst: 1000, CORSOrigins: "*",
	}
	al := alerts.NewService(pool, alerts.LogNotifier{})
	srv := httpapi.New(cfg, pool, catalog.NewRepo(pool), ingest.NewService(pool),
		auth.NewService(pool, cfg.JWTSecret, cfg.JWTTTL), al, nil)
	ts := httptest.NewServer(srv.Routes(ctx))
	t.Cleanup(ts.Close)
	return ts, al
}

type client struct {
	t    *testing.T
	base string
}

func (c client) do(method, path string, headers map[string]string, body any, out any) int {
	c.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.base+path, rd)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	cl := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := cl.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			c.t.Fatalf("%s %s: decode: %v", method, path, err)
		}
	}
	return resp.StatusCode
}

func TestEndToEnd(t *testing.T) {
	ts, al := setup(t)
	c := client{t: t, base: ts.URL}
	admin := map[string]string{"Authorization": "Bearer admin"}

	if code := c.do("POST", "/api/v1/admin/categories", admin, map[string]string{"slug": "tvs", "name": "TVs"}, nil); code != 201 {
		t.Fatalf("create category: %d", code)
	}

	keys := map[string]string{}
	for _, s := range []string{"loja-a", "loja-b"} {
		var out ingest.CreatedStore
		if code := c.do("POST", "/api/v1/admin/stores", admin,
			map[string]any{"slug": s, "name": s, "website": "https://" + s + ".example"}, &out); code != 201 {
			t.Fatalf("create store: %d", code)
		}
		keys[s] = out.APIKey
	}
	if code := c.do("POST", "/api/v1/admin/stores", admin,
		map[string]any{"slug": "loja-a", "name": "x", "website": "https://x.example"}, nil); code != 409 {
		t.Fatalf("loja duplicada deveria dar 409, deu %d", code)
	}

	const ean = "7891000315507"
	ingestOffer := func(store string, price int64, pix *int64) ingest.Result {
		var res ingest.Result
		code := c.do("POST", "/api/v1/ingest/offers", map[string]string{"X-Store-Key": keys[store]},
			map[string]any{"offers": []map[string]any{{
				"sku": "TV-1", "ean": ean, "title": "Smart TV Lumina 55 Polegadas 4K", "brand": "Lumina",
				"category": "tvs", "url": "https://" + store + ".example/tv", "price_cents": price,
				"pix_price_cents": pix, "shipping_cents": 0,
			}}}, &res)
		if code != 200 {
			t.Fatalf("ingest: %d", code)
		}
		return res
	}

	if r := ingestOffer("loja-a", 300000, nil); r.Created != 1 || r.NewProducts != 1 {
		t.Fatalf("primeira ingestão: %+v", r)
	}
	pix := int64(270000)
	if r := ingestOffer("loja-b", 290000, &pix); r.Created != 1 || r.NewProducts != 0 {
		t.Fatalf("mesmo EAN deveria reaproveitar o produto: %+v", r)
	}
	if r := ingestOffer("loja-a", 300000, nil); r.Updated != 1 || r.PriceChanges != 0 {
		t.Fatalf("reenvio sem mudança de preço: %+v", r)
	}

	// Busca com acento ausente e erro de digitação.
	var sr catalog.SearchResult
	c.do("GET", "/api/v1/search?q=lumna", nil, nil, &sr)
	if sr.Total != 1 || sr.CorrectedQuery == nil || *sr.CorrectedQuery != "lumina" {
		t.Fatalf("busca corrigida: total=%d corrected=%v", sr.Total, sr.CorrectedQuery)
	}
	slug := sr.Items[0].Slug
	if got := *sr.Items[0].MinPriceCents; got != 270000 {
		t.Fatalf("menor preço deveria considerar o PIX: %d", got)
	}
	c.do("GET", "/api/v1/search?store=loja-a&max_price=2800", nil, nil, &sr)
	if sr.Total != 1 {
		t.Fatalf("filtro por loja/preço: %d", sr.Total)
	}
	c.do("GET", "/api/v1/search?max_price=2000", nil, nil, &sr)
	if sr.Total != 0 {
		t.Fatalf("filtro preço máximo: %d", sr.Total)
	}

	var p catalog.Product
	if code := c.do("GET", "/api/v1/products/"+slug, nil, nil, &p); code != 200 {
		t.Fatalf("produto: %d", code)
	}
	if len(p.Offers) != 2 || p.Offers[0].StoreSlug != "loja-b" {
		t.Fatalf("ofertas deveriam vir ordenadas pelo menor total: %+v", p.Offers)
	}

	// Cadastro, alerta e disparo.
	var tok struct{ Token string }
	if code := c.do("POST", "/api/v1/auth/register", nil,
		map[string]string{"email": "Ana@Example.com", "name": "Ana", "password": "senha-forte"}, &tok); code != 201 {
		t.Fatalf("register: %d", code)
	}
	if code := c.do("POST", "/api/v1/auth/register", nil,
		map[string]string{"email": "ana@example.com", "name": "Ana", "password": "senha-forte"}, nil); code != 409 {
		t.Fatalf("email duplicado deveria dar 409, deu %d", code)
	}
	user := map[string]string{"Authorization": "Bearer " + tok.Token}
	if code := c.do("POST", "/api/v1/me/alerts", user,
		map[string]any{"product_slug": slug, "target_price_cents": 250000}, nil); code != 201 {
		t.Fatalf("criar alerta: %d", code)
	}

	ctx := context.Background()
	if n, _ := al.CheckAlerts(ctx); n != 0 {
		t.Fatalf("alerta não deveria disparar ainda: %d", n)
	}
	pix = 249990
	ingestOffer("loja-b", 280000, &pix)
	if n, err := al.CheckAlerts(ctx); err != nil || n != 1 {
		t.Fatalf("alerta deveria disparar uma vez: n=%d err=%v", n, err)
	}
	if n, _ := al.CheckAlerts(ctx); n != 0 {
		t.Fatalf("alerta disparou duas vezes: %d", n)
	}
	var notes []alerts.Notification
	c.do("GET", "/api/v1/me/notifications", user, nil, &notes)
	if len(notes) != 1 || !strings.Contains(notes[0].Message, "R$ 2.499,90") {
		t.Fatalf("notificação: %+v", notes)
	}

	// Clique de saída.
	if code := c.do("GET", p.Offers[0].RedirectURL, nil, nil, nil); code != http.StatusFound {
		t.Fatalf("redirect: %d", code)
	}

	// Oferta vencida sai do menor preço.
	if n, err := al.ExpireStaleOffers(ctx, -time.Second); err != nil || n != 2 {
		t.Fatalf("expire: n=%d err=%v", n, err)
	}
	c.do("GET", "/api/v1/products/"+slug, nil, nil, &p)
	if p.MinPriceCents != nil || p.OffersCount != 0 {
		t.Fatalf("produto sem ofertas em estoque: min=%v count=%d", p.MinPriceCents, p.OffersCount)
	}
}
