// Seed popula o banco com dados de demonstração: categorias, lojas fictícias,
// produtos com ofertas em várias lojas e 90 dias de histórico de preço.
package main

import (
	"context"
	"fmt"
	"log"
	"math"
	"math/rand/v2"
	"os"

	"comparai/internal/auth"
	"comparai/internal/db"
	"comparai/internal/ingest"
	"comparai/internal/slug"
)

type product struct {
	title, brand, category string
	basePrice              int64 // centavos
	specs                  map[string]any
}

var categories = []ingest.NewCategory{
	{Slug: "eletronicos", Name: "Eletrônicos"},
	{Slug: "smartphones", Name: "Smartphones", ParentSlug: "eletronicos"},
	{Slug: "notebooks", Name: "Notebooks", ParentSlug: "eletronicos"},
	{Slug: "tvs", Name: "TVs", ParentSlug: "eletronicos"},
	{Slug: "fones", Name: "Fones de ouvido", ParentSlug: "eletronicos"},
	{Slug: "casa", Name: "Casa"},
	{Slug: "eletrodomesticos", Name: "Eletrodomésticos", ParentSlug: "casa"},
	{Slug: "cafeteiras", Name: "Cafeteiras", ParentSlug: "casa"},
	{Slug: "games", Name: "Games"},
	{Slug: "consoles", Name: "Consoles", ParentSlug: "games"},
}

// Lojas e marcas são fictícias.
var stores = []ingest.NewStore{
	{Slug: "mega-varejo", Name: "Mega Varejo", Website: "https://megavarejo.example", Rating: ptr(4.6)},
	{Slug: "loja-do-bairro", Name: "Loja do Bairro", Website: "https://lojadobairro.example", Rating: ptr(4.2)},
	{Slug: "tech-center", Name: "Tech Center", Website: "https://techcenter.example", Rating: ptr(4.8)},
	{Slug: "casa-e-cia", Name: "Casa & Cia", Website: "https://casaecia.example", Rating: ptr(4.1)},
	{Slug: "preco-baixo", Name: "Preço Baixo", Website: "https://precobaixo.example", Rating: ptr(3.9)},
}

var products = []product{
	{"Smartphone Órion X1 128GB 8GB RAM Tela 6,5\"", "Órion", "smartphones", 189900, map[string]any{"armazenamento": "128GB", "ram": "8GB", "tela": "6,5\""}},
	{"Smartphone Órion X1 Pro 256GB 12GB RAM", "Órion", "smartphones", 329900, map[string]any{"armazenamento": "256GB", "ram": "12GB"}},
	{"Smartphone Vértice Neo 5G 128GB", "Vértice", "smartphones", 149900, map[string]any{"armazenamento": "128GB", "rede": "5G"}},
	{"Smartphone Vértice Neo Lite 64GB", "Vértice", "smartphones", 89900, map[string]any{"armazenamento": "64GB"}},
	{"Smartphone Lumina S9 256GB Câmera 108MP", "Lumina", "smartphones", 259900, map[string]any{"camera": "108MP"}},
	{"Notebook Kairo Book 14 Core i5 16GB SSD 512GB", "Kairo", "notebooks", 399900, map[string]any{"processador": "Core i5", "ram": "16GB", "ssd": "512GB"}},
	{"Notebook Kairo Book 15 Ryzen 7 16GB SSD 1TB", "Kairo", "notebooks", 489900, map[string]any{"processador": "Ryzen 7", "ram": "16GB", "ssd": "1TB"}},
	{"Notebook Nimbus Air 13 8GB SSD 256GB", "Nimbus", "notebooks", 279900, map[string]any{"ram": "8GB", "ssd": "256GB"}},
	{"Notebook Gamer Atlas Fury RTX 16GB 1TB", "Atlas", "notebooks", 749900, map[string]any{"gpu": "RTX", "ram": "16GB"}},
	{"Smart TV Lumina 55\" 4K UHD HDR", "Lumina", "tvs", 279900, map[string]any{"tela": "55\"", "resolucao": "4K"}},
	{"Smart TV Lumina 65\" 4K QLED", "Lumina", "tvs", 459900, map[string]any{"tela": "65\"", "painel": "QLED"}},
	{"Smart TV Vértice 43\" Full HD", "Vértice", "tvs", 169900, map[string]any{"tela": "43\"", "resolucao": "Full HD"}},
	{"Smart TV Solaris 50\" 4K Android TV", "Solaris", "tvs", 229900, map[string]any{"tela": "50\""}},
	{"Fone de Ouvido Bluetooth Brisa Pods Cancelamento de Ruído", "Brisa", "fones", 49900, map[string]any{"conexao": "Bluetooth 5.3", "anc": true}},
	{"Fone Headset Gamer Atlas Storm 7.1", "Atlas", "fones", 29900, map[string]any{"som": "7.1"}},
	{"Fone Over-ear Brisa Studio Max", "Brisa", "fones", 129900, map[string]any{"anc": true}},
	{"Geladeira Frost Free Solaris 450L Inox", "Solaris", "eletrodomesticos", 389900, map[string]any{"capacidade": "450L"}},
	{"Máquina de Lavar Solaris 12kg", "Solaris", "eletrodomesticos", 229900, map[string]any{"capacidade": "12kg"}},
	{"Micro-ondas Brisa 30L Inox", "Brisa", "eletrodomesticos", 79900, map[string]any{"capacidade": "30L"}},
	{"Air Fryer Brisa 5,5L Digital", "Brisa", "eletrodomesticos", 39900, map[string]any{"capacidade": "5,5L"}},
	{"Aspirador Robô Nimbus Clean Mapeamento a Laser", "Nimbus", "eletrodomesticos", 159900, nil},
	{"Cafeteira Espresso Kairo Barista 20 bar", "Kairo", "cafeteiras", 89900, map[string]any{"pressao": "20 bar"}},
	{"Cafeteira de Cápsulas Kairo Mini", "Kairo", "cafeteiras", 34900, nil},
	{"Cafeteira Elétrica Brisa 30 Xícaras", "Brisa", "cafeteiras", 14900, nil},
	{"Console Atlas Station 5 1TB", "Atlas", "consoles", 399900, map[string]any{"armazenamento": "1TB"}},
	{"Console Atlas Station 5 Digital", "Atlas", "consoles", 349900, nil},
	{"Console Portátil Nimbus Go 512GB", "Nimbus", "consoles", 299900, map[string]any{"armazenamento": "512GB"}},
	{"Controle Sem Fio Atlas Station Preto", "Atlas", "consoles", 44900, nil},
}

func ptr[T any](v T) *T { return &v }

// ean13 gera um EAN-13 válido com prefixo brasileiro (789).
func ean13(n int) string {
	base := fmt.Sprintf("789%09d", 100000000+n)
	sum := 0
	for i, c := range base {
		d := int(c - '0')
		if i%2 == 1 {
			d *= 3
		}
		sum += d
	}
	return fmt.Sprintf("%s%d", base, (10-sum%10)%10)
}

func round90(c int64) int64 { return (c/100)*100 - 10 } // ex.: 1899,90

func main() {
	ctx := context.Background()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		url = "postgres://postgres:postgres@localhost:5432/comparai?sslmode=disable"
	}
	pool, err := db.Connect(ctx, url, 5)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool); err != nil {
		log.Fatal(err)
	}

	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM stores`).Scan(&n)
	if n > 0 {
		fmt.Println("banco já tem lojas; seed ignorado (rode em um banco vazio)")
		return
	}

	ing := ingest.NewService(pool)
	for _, c := range categories {
		if _, err := ing.CreateCategory(ctx, c); err != nil {
			log.Fatalf("categoria %s: %v", c.Slug, err)
		}
	}

	rng := rand.New(rand.NewPCG(42, 7))
	fmt.Println("Lojas criadas (guarde as chaves para testar /api/v1/ingest/offers):")
	for si, st := range stores {
		created, err := ing.CreateStore(ctx, st)
		if err != nil {
			log.Fatalf("loja %s: %v", st.Slug, err)
		}
		fmt.Printf("  %-16s X-Store-Key: %s\n", created.Slug, created.APIKey)

		var batch []ingest.OfferInput
		for pi, p := range products {
			// Nem toda loja vende tudo.
			if rng.IntN(100) < 25 && si > 0 {
				continue
			}
			factor := 0.9 + rng.Float64()*0.25 // -10% a +15%
			price := round90(int64(float64(p.basePrice) * factor))
			list := round90(int64(float64(price) * (1.1 + rng.Float64()*0.2)))
			pix := price - price*int64(5+rng.IntN(6))/100 // 5–10% off no PIX
			inst := 10
			if price < 20000 {
				inst = 3
			}
			var ship *int64
			switch rng.IntN(3) {
			case 0:
				ship = ptr(int64(0))
			case 1:
				ship = ptr(int64(1990 + rng.IntN(4)*1000))
			}
			sku := fmt.Sprintf("%s-%04d", st.Slug[:3], pi)
			batch = append(batch, ingest.OfferInput{
				SKU: sku, EAN: ean13(pi), Title: p.title, Brand: p.brand, Category: p.category,
				URL:        fmt.Sprintf("%s/p/%s", st.Website, slug.Make(p.title)),
				Specs:      p.specs,
				PriceCents: price, ListPriceCents: &list, PixPriceCents: &pix,
				Installments: &inst, InstallmentCents: ptr(int64(math.Ceil(float64(price) / float64(inst)))),
				ShippingCents: ship,
			})
		}
		res, err := ing.Import(ctx, created.ID, batch)
		if err != nil {
			log.Fatalf("import %s: %v", st.Slug, err)
		}
		if len(res.Rejected) > 0 {
			log.Fatalf("import %s rejeitou itens: %+v", st.Slug, res.Rejected)
		}
	}

	// Histórico sintético: passeio aleatório nos 90 dias anteriores.
	// Parte dos produtos estava mais cara no passado, gerando "ofertas do dia".
	_, err = pool.Exec(ctx, `
		INSERT INTO product_price_daily (product_id, day, min_price_cents)
		SELECT p.id, current_date - g.d,
		       (p.min_price_cents * (
		           CASE WHEN p.id % 3 = 0 THEN 1.12 ELSE 1.0 END
		           + 0.06 * sin(g.d / 7.0 + p.id)
		           + 0.02 * ((p.id * 31 + g.d * 17) % 7 - 3) / 3.0
		       ))::bigint
		FROM products p, generate_series(1, 90) AS g(d)
		WHERE p.min_price_cents IS NOT NULL
		ON CONFLICT DO NOTHING`)
	if err != nil {
		log.Fatalf("histórico: %v", err)
	}

	au := auth.NewService(pool, "seed-only-secret-not-used-for-tokens", 0)
	if _, err := au.Register(ctx, "demo@comparai.dev", "Usuário Demo", "demo12345"); err != nil {
		log.Fatalf("usuário demo: %v", err)
	}
	fmt.Printf("\n%d produtos, %d lojas. Usuário demo: demo@comparai.dev / demo12345\n", len(products), len(stores))
}
