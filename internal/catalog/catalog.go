// Package catalog expõe as consultas de leitura do comparador:
// busca, detalhe do produto, histórico de preço, categorias, lojas e ofertas do dia.
package catalog

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNotFound = errors.New("not found")

type Repo struct {
	db *pgxpool.Pool
}

func NewRepo(db *pgxpool.Pool) *Repo { return &Repo{db: db} }

// ---------- modelos ----------

type ProductSummary struct {
	ID            int64   `json:"id"`
	Slug          string  `json:"slug"`
	Title         string  `json:"title"`
	Brand         *string `json:"brand"`
	ImageURL      *string `json:"image_url"`
	CategorySlug  *string `json:"category_slug"`
	MinPriceCents *int64  `json:"min_price_cents"`
	MaxPriceCents *int64  `json:"max_price_cents"`
	OffersCount   int     `json:"offers_count"`
}

type Offer struct {
	ID               int64     `json:"id"`
	StoreSlug        string    `json:"store_slug"`
	StoreName        string    `json:"store_name"`
	StoreLogoURL     *string   `json:"store_logo_url"`
	StoreRating      *float64  `json:"store_rating"`
	Title            string    `json:"title"`
	PriceCents       int64     `json:"price_cents"`
	ListPriceCents   *int64    `json:"list_price_cents"`
	PixPriceCents    *int64    `json:"pix_price_cents"`
	Installments     *int      `json:"installments"`
	InstallmentCents *int64    `json:"installment_cents"`
	ShippingCents    *int64    `json:"shipping_cents"`
	BestPriceCents   int64     `json:"best_price_cents"` // menor entre preço normal e PIX
	TotalCents       *int64    `json:"total_cents"`      // melhor preço + frete (se frete conhecido)
	InStock          bool      `json:"in_stock"`
	RedirectURL      string    `json:"redirect_url"`
	LastSeenAt       time.Time `json:"last_seen_at"`
}

type Product struct {
	ProductSummary
	EAN          *string        `json:"ean"`
	Description  *string        `json:"description"`
	Specs        map[string]any `json:"specs"`
	CategoryName *string        `json:"category_name"`
	Offers       []Offer        `json:"offers"`
	Stats        PriceStats     `json:"stats"`
}

type PriceStats struct {
	Lowest90dCents  *int64 `json:"lowest_90d_cents"`
	Average30dCents *int64 `json:"average_30d_cents"`
	// Rótulo amigável: "abaixo da média", "na média", "acima da média".
	Verdict string `json:"verdict"`
}

type PricePoint struct {
	Day           string `json:"day"`
	MinPriceCents int64  `json:"min_price_cents"`
}

type Category struct {
	ID       int64      `json:"id"`
	Slug     string     `json:"slug"`
	Name     string     `json:"name"`
	ParentID *int64     `json:"parent_id,omitempty"`
	Children []Category `json:"children,omitempty"`
}

type Store struct {
	ID          int64    `json:"id"`
	Slug        string   `json:"slug"`
	Name        string   `json:"name"`
	Website     string   `json:"website"`
	LogoURL     *string  `json:"logo_url"`
	Rating      *float64 `json:"rating"`
	OffersCount int      `json:"offers_count"`
}

type Deal struct {
	ProductSummary
	Average30dCents int64   `json:"average_30d_cents"`
	DropPercent     float64 `json:"drop_percent"`
}

// ---------- busca ----------

type Sort string

const (
	SortRelevance Sort = "relevance"
	SortPriceAsc  Sort = "price_asc"
	SortPriceDesc Sort = "price_desc"
	SortOffers    Sort = "offers"
	SortNewest    Sort = "newest"
)

type SearchParams struct {
	Query         string
	CategorySlug  string
	Brands        []string
	StoreSlugs    []string
	MinPriceCents int64
	MaxPriceCents int64
	Sort          Sort
	Page          int
	PerPage       int
}

type Facet struct {
	Value string `json:"value"`
	Label string `json:"label"`
	Count int    `json:"count"`
}

type SearchResult struct {
	Query          string           `json:"query"`
	CorrectedQuery *string          `json:"corrected_query"` // preenchido quando a busca foi corrigida
	Items          []ProductSummary `json:"items"`
	Total          int              `json:"total"`
	Page           int              `json:"page"`
	PerPage        int              `json:"per_page"`
	Brands         []Facet          `json:"brands"`
	Categories     []Facet          `json:"categories"`
	Stores         []Facet          `json:"stores"`
	PriceRange     struct {
		MinCents *int64 `json:"min_cents"`
		MaxCents *int64 `json:"max_cents"`
	} `json:"price_range"`
}

// queryBuilder acumula cláusulas WHERE com placeholders numerados.
type queryBuilder struct {
	where []string
	args  []any
}

func (q *queryBuilder) arg(v any) string {
	q.args = append(q.args, v)
	return fmt.Sprintf("$%d", len(q.args))
}

func (q *queryBuilder) add(clause string) { q.where = append(q.where, clause) }

func (q *queryBuilder) sql() string {
	if len(q.where) == 0 {
		return "TRUE"
	}
	return strings.Join(q.where, " AND ")
}

const summaryCols = `p.id, p.slug, p.title, p.brand, p.image_url, c.slug, p.min_price_cents, p.max_price_cents, p.offers_count`

func scanSummary(row pgx.Row, extra ...any) (ProductSummary, error) {
	var s ProductSummary
	dst := append([]any{&s.ID, &s.Slug, &s.Title, &s.Brand, &s.ImageURL, &s.CategorySlug,
		&s.MinPriceCents, &s.MaxPriceCents, &s.OffersCount}, extra...)
	err := row.Scan(dst...)
	return s, err
}

// Search busca produtos. Se a busca original não encontra nada, tenta de novo
// com cada termo corrigido para a palavra mais parecida do catálogo.
func (r *Repo) Search(ctx context.Context, p SearchParams) (SearchResult, error) {
	res, err := r.search(ctx, p)
	if err != nil || res.Total > 0 || strings.TrimSpace(p.Query) == "" {
		return res, err
	}
	corrected, err := r.correct(ctx, p.Query)
	if err != nil || corrected == "" || corrected == normalize(p.Query) {
		return res, err
	}
	p2 := p
	p2.Query = corrected
	res2, err := r.search(ctx, p2)
	if err != nil || res2.Total == 0 {
		return res, err
	}
	res2.Query = p.Query
	res2.CorrectedQuery = &corrected
	return res2, nil
}

func normalize(q string) string { return strings.Join(strings.Fields(strings.ToLower(q)), " ") }

// correct troca cada termo desconhecido pela palavra mais similar (trigram) do dicionário.
func (r *Repo) correct(ctx context.Context, q string) (string, error) {
	var out *string
	err := r.db.QueryRow(ctx, `
		SELECT string_agg(coalesce(exact.word, best.word, t.w), ' ' ORDER BY t.ord)
		FROM unnest(regexp_split_to_array(f_unaccent(lower(trim($1))), '\s+')) WITH ORDINALITY AS t(w, ord)
		LEFT JOIN search_words exact ON exact.word = t.w
		LEFT JOIN LATERAL (
			SELECT sw.word FROM search_words sw
			WHERE exact.word IS NULL AND length(t.w) >= 3 AND sw.word % t.w
			ORDER BY similarity(sw.word, t.w) DESC, sw.ndoc DESC
			LIMIT 1
		) best ON true`, q).Scan(&out)
	if err != nil || out == nil {
		return "", err
	}
	return *out, nil
}

func (r *Repo) search(ctx context.Context, p SearchParams) (SearchResult, error) {
	res := SearchResult{Query: p.Query, Page: p.Page, PerPage: p.PerPage, Items: []ProductSummary{}}
	qb := &queryBuilder{}

	// Só mostramos produtos com pelo menos uma oferta em estoque.
	qb.add("p.min_price_cents IS NOT NULL")

	rank := "0"
	if q := strings.TrimSpace(p.Query); q != "" {
		qa := qb.arg(q)
		// Full-text (stemming PT-BR, sem acento) OU similaridade trigram (tolera erro de digitação).
		qb.add(fmt.Sprintf(`(p.search @@ websearch_to_tsquery('portuguese', f_unaccent(%[1]s))
			OR f_unaccent(lower(p.title)) %% f_unaccent(lower(%[1]s)))`, qa))
		rank = fmt.Sprintf(`(ts_rank(p.search, websearch_to_tsquery('portuguese', f_unaccent(%[1]s)))
			+ similarity(f_unaccent(lower(p.title)), f_unaccent(lower(%[1]s))))`, qa)
	}
	if p.CategorySlug != "" {
		// Inclui subcategorias.
		qb.add(fmt.Sprintf(`p.category_id IN (
			WITH RECURSIVE tree AS (
				SELECT id FROM categories WHERE slug = %s
				UNION ALL SELECT c2.id FROM categories c2 JOIN tree t ON c2.parent_id = t.id)
			SELECT id FROM tree)`, qb.arg(p.CategorySlug)))
	}
	if len(p.Brands) > 0 {
		lower := make([]string, len(p.Brands))
		for i, b := range p.Brands {
			lower[i] = strings.ToLower(b)
		}
		qb.add(fmt.Sprintf("lower(p.brand) = ANY(%s)", qb.arg(lower)))
	}
	if len(p.StoreSlugs) > 0 {
		qb.add(fmt.Sprintf(`EXISTS (SELECT 1 FROM offers o JOIN stores s ON s.id = o.store_id
			WHERE o.product_id = p.id AND o.in_stock AND s.active AND s.slug = ANY(%s))`, qb.arg(p.StoreSlugs)))
	}
	if p.MinPriceCents > 0 {
		qb.add(fmt.Sprintf("p.min_price_cents >= %s", qb.arg(p.MinPriceCents)))
	}
	if p.MaxPriceCents > 0 {
		qb.add(fmt.Sprintf("p.min_price_cents <= %s", qb.arg(p.MaxPriceCents)))
	}

	order := "p.offers_count DESC, p.id DESC"
	switch p.Sort {
	case SortPriceAsc:
		order = "p.min_price_cents ASC, p.id"
	case SortPriceDesc:
		order = "p.min_price_cents DESC, p.id"
	case SortOffers:
		order = "p.offers_count DESC, p.min_price_cents ASC"
	case SortNewest:
		order = "p.created_at DESC"
	default:
		if rank != "0" {
			order = rank + " DESC, p.offers_count DESC"
		}
	}

	where := qb.sql()
	from := "FROM products p LEFT JOIN categories c ON c.id = p.category_id WHERE " + where
	limitArg := qb.arg(p.PerPage)
	offsetArg := qb.arg((p.Page - 1) * p.PerPage)

	// Todas as consultas usam a mesma lista de argumentos; as de facets ignoram limit/offset
	// via referência explícita, então mandamos tudo num único batch (um round-trip).
	b := &pgx.Batch{}
	b.Queue(fmt.Sprintf(`SELECT %s, count(*) OVER() %s ORDER BY %s LIMIT %s OFFSET %s`,
		summaryCols, from, order, limitArg, offsetArg), qb.args...)
	facetArgs := qb.args[:len(qb.args)-2]
	b.Queue(fmt.Sprintf(`SELECT p.brand, count(*) %s AND p.brand IS NOT NULL
		GROUP BY p.brand ORDER BY count(*) DESC, p.brand LIMIT 20`, from), facetArgs...)
	b.Queue(fmt.Sprintf(`SELECT c.slug, c.name, count(*) %s AND c.id IS NOT NULL
		GROUP BY c.slug, c.name ORDER BY count(*) DESC LIMIT 20`, from), facetArgs...)
	b.Queue(fmt.Sprintf(`SELECT s.slug, s.name, count(DISTINCT p.id)
		%s AND o.in_stock AND s.active GROUP BY s.slug, s.name ORDER BY 3 DESC LIMIT 20`,
		"FROM products p LEFT JOIN categories c ON c.id = p.category_id JOIN offers o ON o.product_id = p.id JOIN stores s ON s.id = o.store_id WHERE "+where),
		facetArgs...)
	b.Queue(fmt.Sprintf(`SELECT min(p.min_price_cents), max(p.min_price_cents) %s`, from), facetArgs...)

	br := r.db.SendBatch(ctx, b)
	defer br.Close()

	rows, err := br.Query()
	if err != nil {
		return res, fmt.Errorf("search items: %w", err)
	}
	for rows.Next() {
		s, err := scanSummary(rows, &res.Total)
		if err != nil {
			rows.Close()
			return res, err
		}
		res.Items = append(res.Items, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return res, err
	}
	if len(res.Items) == 0 && p.Page > 1 {
		// count(*) OVER() não retorna nada numa página vazia; o total vem do facet de preço abaixo.
		res.Total = -1
	}

	if res.Brands, err = scanFacets(br, false); err != nil {
		return res, fmt.Errorf("brand facets: %w", err)
	}
	if res.Categories, err = scanFacets(br, true); err != nil {
		return res, fmt.Errorf("category facets: %w", err)
	}
	if res.Stores, err = scanFacets(br, true); err != nil {
		return res, fmt.Errorf("store facets: %w", err)
	}
	if err := br.QueryRow().Scan(&res.PriceRange.MinCents, &res.PriceRange.MaxCents); err != nil {
		return res, fmt.Errorf("price range: %w", err)
	}
	if res.Total == -1 {
		if err := r.db.QueryRow(ctx, "SELECT count(*) "+from, facetArgs...).Scan(&res.Total); err != nil {
			return res, err
		}
	}
	return res, nil
}

func scanFacets(br pgx.BatchResults, withLabel bool) ([]Facet, error) {
	rows, err := br.Query()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Facet{}
	for rows.Next() {
		var f Facet
		if withLabel {
			err = rows.Scan(&f.Value, &f.Label, &f.Count)
		} else {
			err = rows.Scan(&f.Value, &f.Count)
			f.Label = f.Value
		}
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// Suggest devolve títulos para autocomplete.
func (r *Repo) Suggest(ctx context.Context, q string, limit int) ([]ProductSummary, error) {
	rows, err := r.db.Query(ctx, `
		SELECT `+summaryCols+`
		FROM products p LEFT JOIN categories c ON c.id = p.category_id
		WHERE p.min_price_cents IS NOT NULL
		  AND (f_unaccent(lower(p.title)) LIKE '%' || f_unaccent(lower($1)) || '%'
		       OR f_unaccent(lower(p.title)) % f_unaccent(lower($1)))
		ORDER BY similarity(f_unaccent(lower(p.title)), f_unaccent(lower($1))) DESC, p.offers_count DESC
		LIMIT $2`, q, limit)
	if err != nil {
		return nil, err
	}
	return collectSummaries(rows)
}

func collectSummaries(rows pgx.Rows) ([]ProductSummary, error) {
	defer rows.Close()
	out := []ProductSummary{}
	for rows.Next() {
		s, err := scanSummary(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ---------- produto ----------

func (r *Repo) ProductBySlug(ctx context.Context, slug string) (Product, error) {
	var p Product
	row := r.db.QueryRow(ctx, `
		SELECT `+summaryCols+`, p.ean, p.description, p.specs, c.name
		FROM products p LEFT JOIN categories c ON c.id = p.category_id
		WHERE p.slug = $1`, slug)
	s, err := scanSummary(row, &p.EAN, &p.Description, &p.Specs, &p.CategoryName)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrNotFound
	}
	if err != nil {
		return p, err
	}
	p.ProductSummary = s

	if p.Offers, err = r.offers(ctx, p.ID); err != nil {
		return p, err
	}
	err = r.db.QueryRow(ctx, `
		SELECT
		  (SELECT min(min_price_cents) FROM product_price_daily WHERE product_id = $1 AND day >= current_date - 90),
		  (SELECT avg(min_price_cents)::bigint FROM product_price_daily WHERE product_id = $1 AND day >= current_date - 30)`,
		p.ID).Scan(&p.Stats.Lowest90dCents, &p.Stats.Average30dCents)
	if err != nil {
		return p, err
	}
	p.Stats.Verdict = verdict(p.MinPriceCents, p.Stats.Average30dCents)
	return p, nil
}

// verdict compara o preço atual com a média de 30 dias (faixa de ±3%).
func verdict(current, avg *int64) string {
	if current == nil || avg == nil || *avg == 0 {
		return "sem histórico"
	}
	ratio := float64(*current) / float64(*avg)
	switch {
	case ratio <= 0.97:
		return "abaixo da média"
	case ratio >= 1.03:
		return "acima da média"
	default:
		return "na média"
	}
}

func (r *Repo) offers(ctx context.Context, productID int64) ([]Offer, error) {
	rows, err := r.db.Query(ctx, `
		SELECT o.id, s.slug, s.name, s.logo_url, s.rating::float8, o.title, o.price_cents, o.list_price_cents,
		       o.pix_price_cents, o.installments, o.installment_cents, o.shipping_cents, o.in_stock, o.last_seen_at,
		       LEAST(o.price_cents, coalesce(o.pix_price_cents, o.price_cents)) AS best
		FROM offers o JOIN stores s ON s.id = o.store_id
		WHERE o.product_id = $1 AND s.active
		ORDER BY o.in_stock DESC,
		         LEAST(o.price_cents, coalesce(o.pix_price_cents, o.price_cents)) + coalesce(o.shipping_cents, 0) ASC`, productID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Offer{}
	for rows.Next() {
		var o Offer
		if err := rows.Scan(&o.ID, &o.StoreSlug, &o.StoreName, &o.StoreLogoURL, &o.StoreRating, &o.Title,
			&o.PriceCents, &o.ListPriceCents, &o.PixPriceCents, &o.Installments, &o.InstallmentCents,
			&o.ShippingCents, &o.InStock, &o.LastSeenAt, &o.BestPriceCents); err != nil {
			return nil, err
		}
		if o.ShippingCents != nil {
			t := o.BestPriceCents + *o.ShippingCents
			o.TotalCents = &t
		}
		o.RedirectURL = fmt.Sprintf("/r/%d", o.ID)
		out = append(out, o)
	}
	return out, rows.Err()
}

func (r *Repo) PriceHistory(ctx context.Context, slug string, days int) ([]PricePoint, error) {
	rows, err := r.db.Query(ctx, `
		SELECT d.day::text, d.min_price_cents
		FROM product_price_daily d JOIN products p ON p.id = d.product_id
		WHERE p.slug = $1 AND d.day >= current_date - $2::int
		ORDER BY d.day`, slug, days)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PricePoint{}
	for rows.Next() {
		var pp PricePoint
		if err := rows.Scan(&pp.Day, &pp.MinPriceCents); err != nil {
			return nil, err
		}
		out = append(out, pp)
	}
	return out, rows.Err()
}

// ProductIDBySlug é usado por favoritos e alertas.
func (r *Repo) ProductIDBySlug(ctx context.Context, slug string) (int64, error) {
	var id int64
	err := r.db.QueryRow(ctx, `SELECT id FROM products WHERE slug = $1`, slug).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	return id, err
}

// ---------- categorias, lojas, ofertas do dia ----------

func (r *Repo) CategoryTree(ctx context.Context) ([]Category, error) {
	rows, err := r.db.Query(ctx, `SELECT id, slug, name, parent_id FROM categories ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var all []Category
	for rows.Next() {
		var c Category
		if err := rows.Scan(&c.ID, &c.Slug, &c.Name, &c.ParentID); err != nil {
			return nil, err
		}
		all = append(all, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return buildTree(all), nil
}

func buildTree(all []Category) []Category {
	children := map[int64][]Category{}
	var roots []Category
	for _, c := range all {
		if c.ParentID == nil {
			roots = append(roots, c)
		} else {
			children[*c.ParentID] = append(children[*c.ParentID], c)
		}
	}
	var attach func(c Category) Category
	attach = func(c Category) Category {
		for _, ch := range children[c.ID] {
			c.Children = append(c.Children, attach(ch))
		}
		return c
	}
	out := make([]Category, 0, len(roots))
	for _, r := range roots {
		out = append(out, attach(r))
	}
	return out
}

func (r *Repo) Stores(ctx context.Context) ([]Store, error) {
	rows, err := r.db.Query(ctx, `
		SELECT s.id, s.slug, s.name, s.website, s.logo_url, s.rating::float8,
		       (SELECT count(*) FROM offers o WHERE o.store_id = s.id AND o.in_stock)
		FROM stores s WHERE s.active ORDER BY s.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Store{}
	for rows.Next() {
		var s Store
		if err := rows.Scan(&s.ID, &s.Slug, &s.Name, &s.Website, &s.LogoURL, &s.Rating, &s.OffersCount); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// Deals lista produtos cujo menor preço atual está mais abaixo da média dos últimos 30 dias.
func (r *Repo) Deals(ctx context.Context, categorySlug string, limit int) ([]Deal, error) {
	rows, err := r.db.Query(ctx, `
		SELECT `+summaryCols+`, a.avg30,
		       round(100.0 * (a.avg30 - p.min_price_cents) / a.avg30, 1)::float8 AS drop
		FROM products p
		LEFT JOIN categories c ON c.id = p.category_id
		JOIN LATERAL (
			SELECT avg(d.min_price_cents)::bigint AS avg30
			FROM product_price_daily d
			WHERE d.product_id = p.id AND d.day >= current_date - 30 AND d.day < current_date
		) a ON a.avg30 IS NOT NULL
		WHERE p.min_price_cents IS NOT NULL
		  AND p.min_price_cents < a.avg30
		  AND ($1 = '' OR c.slug = $1)
		ORDER BY drop DESC
		LIMIT $2`, categorySlug, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Deal{}
	for rows.Next() {
		var d Deal
		s, err := scanSummary(rows, &d.Average30dCents, &d.DropPercent)
		if err != nil {
			return nil, err
		}
		d.ProductSummary = s
		out = append(out, d)
	}
	return out, rows.Err()
}

// OfferRedirect devolve a URL da loja para o clique de saída.
func (r *Repo) OfferURL(ctx context.Context, offerID int64) (string, error) {
	var url string
	err := r.db.QueryRow(ctx, `
		SELECT o.url FROM offers o JOIN stores s ON s.id = o.store_id
		WHERE o.id = $1 AND s.active`, offerID).Scan(&url)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return url, err
}

func (r *Repo) RecordClick(ctx context.Context, offerID int64, userID *int64, ipHash, ua, referer string) error {
	_, err := r.db.Exec(ctx, `INSERT INTO clicks (offer_id, user_id, ip_hash, user_agent, referer) VALUES ($1,$2,$3,$4,$5)`,
		offerID, userID, ipHash, truncate(ua, 300), truncate(referer, 500))
	return err
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
