package crawler

import (
	"net/url"
	"testing"
)

func TestParseSitemap(t *testing.T) {
	urls, err := ParseSitemap([]byte(`<urlset><url><loc>https://l.example/a</loc></url></urlset>`))
	if err != nil || len(urls) != 1 || urls[0] != "https://l.example/a" {
		t.Fatalf("urlset: %v %v", urls, err)
	}
	idx, err := ParseSitemap([]byte(`<sitemapindex><sitemap><loc>https://l.example/s1.xml</loc></sitemap></sitemapindex>`))
	if err != nil || len(idx) != 1 || idx[0] != "https://l.example/s1.xml" {
		t.Fatalf("sitemapindex: %v %v", idx, err)
	}
}

func TestParsePrice(t *testing.T) {
	cases := []struct {
		in   any
		want int64
		ok   bool
	}{
		{1299.9, 129990, true},
		{"1299.90", 129990, true},
		{"R$ 1.299,90", 129990, true},
		{"", 0, false},
		{"abc", 0, false},
		{-5.0, 0, false},
	}
	for _, c := range cases {
		got, ok := parsePrice(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("parsePrice(%v) = %d, %v; esperado %d, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestExtractProductsFromGraphAndIgnoresBrokenJSON(t *testing.T) {
	page := `<html><head>
	<script type="application/ld+json">{"@graph":[{"@type":"Thing"},{"@type":["Product","Thing"],"name":"X"}]}</script>
	<script type="application/ld+json">{quebrado</script>
	</head></html>`
	got := ExtractProducts([]byte(page))
	if len(got) != 1 || got[0]["name"] != "X" {
		t.Fatalf("produtos: %v", got)
	}
}

func TestToOffersDropsInvalidEANButKeepsOffer(t *testing.T) {
	page, _ := url.Parse("https://loja.example/p/1")
	p := map[string]any{
		"name": "TV", "gtin13": "1234567890123",
		"offers": map[string]any{"price": "10.00", "priceCurrency": "BRL", "availability": "https://schema.org/OutOfStock"},
	}
	offers := ToOffers(p, page)
	if len(offers) != 1 {
		t.Fatalf("ofertas: %d", len(offers))
	}
	o := offers[0]
	if o.EAN != "" || o.PriceCents != 1000 || o.InStock == nil || *o.InStock {
		t.Fatalf("oferta: %+v", o)
	}
}

func TestToOffersRejectsNonHTTPAndWrongCurrency(t *testing.T) {
	page, _ := url.Parse("https://loja.example/p/1")
	p := map[string]any{
		"name": "TV",
		"offers": []any{
			map[string]any{"price": "10", "priceCurrency": "USD"},
			map[string]any{"price": "10", "priceCurrency": "BRL", "url": "javascript:alert(1)"},
			map[string]any{"price": "12", "priceCurrency": "BRL"},
		},
	}
	offers := ToOffers(p, page)
	if len(offers) != 1 || offers[0].PriceCents != 1200 {
		t.Fatalf("ofertas filtradas: %+v", offers)
	}
}

func TestExtractLinksResolvesRelativeAndDropsFragmentOnlyLater(t *testing.T) {
	base, _ := url.Parse("https://loja.example/ofertas/")
	links := ExtractLinks([]byte(`<a href="../produto/1?x=2">p</a><a href="#topo">t</a>`), base)
	want := []string{"https://loja.example/produto/1?x=2", "https://loja.example/ofertas/#topo"}
	if len(links) != len(want) || links[0] != want[0] || links[1] != want[1] {
		t.Fatalf("links: %v", links)
	}
}
