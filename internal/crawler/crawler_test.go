package crawler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"comparai/internal/db"
	"comparai/internal/ingest"
)

// productPage monta uma página de produto com JSON-LD (o formato que as lojas já publicam para o Google).
func productPage(name, gtin, price string) string {
	return fmt.Sprintf(`<!doctype html><html><head>
<script type="application/ld+json">{"@context":"https://schema.org","@graph":[{"@type":"WebSite","name":"loja"},
{"@type":"Product","name":%q,"brand":{"@type":"Brand","name":"Lumina"},"gtin13":%q,"sku":"SKU-%s",
 "offers":{"@type":"Offer","price":%q,"priceCurrency":"BRL","availability":"https://schema.org/InStock"}}]}</script>
</head><body><h1>%s</h1></body></html>`, name, gtin, gtin, price, name)
}

// TestCrawlEndToEnd roda o crawler contra um site local: robots.txt, sitemap, listagem,
// produtos com e sem EAN válido, página 404 e uma seção bloqueada.
// Precisa de TEST_DATABASE_URL (o schema public é apagado).
func TestCrawlEndToEnd(t *testing.T) {
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("TEST_DATABASE_URL não definido")
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, dbURL, 5)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public;`); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	hits := map[string]int{}
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits[r.URL.Path]++
		mu.Unlock()
		base := "http://" + r.Host
		switch r.URL.Path {
		case "/robots.txt":
			fmt.Fprint(w, "User-agent: *\nDisallow: /privado/\n")
		case "/sitemap.xml":
			w.Header().Set("Content-Type", "application/xml")
			fmt.Fprintf(w, `<?xml version="1.0"?><urlset><url><loc>%s/ofertas</loc></url><url><loc>%s/privado/x</loc></url></urlset>`, base, base)
		case "/ofertas":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, `<html><body>
				<a href="/produto/a">A</a>
				<a href="/produto/b?ref=home#topo">B</a>
				<a href="/produto/c">C</a>
				<a href="/privado/x">X</a>
				<a href="https://outra-loja.example/p">externo</a></body></html>`)
		case "/produto/a":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, productPage("Smart TV Lumina 55 4K", "7891000315507", "1299.90"))
		case "/produto/b":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			// EAN inválido (dígito verificador errado) e preço no formato brasileiro.
			fmt.Fprint(w, productPage("Smart TV Lumina 43 Full HD", "1234567890123", "1.049,90"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer site.Close()

	ing := ingest.NewService(pool)
	st, err := ing.CreateStore(ctx, ingest.NewStore{Slug: "loja-teste", Name: "Loja Teste", Website: site.URL})
	if err != nil {
		t.Fatal(err)
	}
	src := Source{
		StoreID: st.ID, Host: strings.TrimPrefix(site.URL, "http://"),
		RPS: 5, RefreshMinutes: 60, MaxDepth: 3, MaxURLs: 100,
	}
	if err := AddSource(ctx, pool, src, site.URL+"/sitemap.xml"); err != nil {
		t.Fatal(err)
	}

	w := NewWorker(pool, "ComparaiBot/1.0 (+teste)", ing)
	n, err := w.Drain(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// sitemap, /ofertas, /privado/x (bloqueada), /produto/a, /produto/b, /produto/c
	if n != 6 {
		t.Fatalf("páginas processadas: %d, esperado 6", n)
	}

	mu.Lock()
	if hits["/privado/x"] != 0 {
		t.Errorf("robots.txt foi ignorado: /privado/x recebeu %d requisições", hits["/privado/x"])
	}
	if hits["/produto/a"] != 1 {
		t.Errorf("/produto/a deveria ser buscado uma vez, foram %d", hits["/produto/a"])
	}
	mu.Unlock()

	var offers int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM offers WHERE store_id = $1`, st.ID).Scan(&offers); err != nil {
		t.Fatal(err)
	}
	if offers != 2 {
		t.Fatalf("ofertas importadas: %d, esperado 2", offers)
	}

	var priceA, priceB int64
	var eanA, eanB *string
	pool.QueryRow(ctx, `SELECT o.price_cents, p.ean FROM offers o JOIN products p ON p.id = o.product_id WHERE o.url = $1`,
		site.URL+"/produto/a").Scan(&priceA, &eanA)
	pool.QueryRow(ctx, `SELECT o.price_cents, p.ean FROM offers o JOIN products p ON p.id = o.product_id WHERE o.url = $1`,
		site.URL+"/produto/b?ref=home").Scan(&priceB, &eanB)
	if priceA != 129990 {
		t.Errorf("preço A: %d", priceA)
	}
	if priceB != 104990 {
		t.Errorf("preço B (formato pt-BR): %d", priceB)
	}
	if eanA == nil || *eanA != "7891000315507" {
		t.Errorf("EAN A: %v", eanA)
	}
	if eanB != nil {
		t.Errorf("EAN inválido deveria ser descartado, veio %v", *eanB)
	}

	status := func(u string) string {
		var s string
		if err := pool.QueryRow(ctx, `SELECT status FROM crawl_urls WHERE url = $1`, u).Scan(&s); err != nil {
			t.Fatalf("status de %s: %v", u, err)
		}
		return s
	}
	if s := status(site.URL + "/produto/c"); s != "gone" {
		t.Errorf("/produto/c: status %s, esperado gone", s)
	}
	if s := status(site.URL + "/privado/x"); s != "skipped" {
		t.Errorf("/privado/x: status %s, esperado skipped", s)
	}

	// Nada vencido: a segunda rodada não deve processar nada (agendamento de 60 min).
	n2, err := w.Drain(ctx)
	if err != nil || n2 != 0 {
		t.Fatalf("segunda rodada: n=%d err=%v", n2, err)
	}
}
