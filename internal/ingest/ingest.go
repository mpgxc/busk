// Package ingest recebe feeds de ofertas das lojas parceiras e mantém
// produtos, ofertas, histórico de preço e o menor preço diário consistentes.
package ingest

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"comparai/internal/slug"
)

const MaxBatch = 1000

var (
	ErrInvalidKey = errors.New("invalid store api key")
	ErrBatchSize  = fmt.Errorf("batch must contain between 1 and %d offers", MaxBatch)
)

// OfferInput é um item do feed. Preços sempre em centavos.
type OfferInput struct {
	SKU              string         `json:"sku"`
	EAN              string         `json:"ean,omitempty"`
	Title            string         `json:"title"`
	Brand            string         `json:"brand,omitempty"`
	Category         string         `json:"category,omitempty"` // slug da categoria
	URL              string         `json:"url"`
	ImageURL         string         `json:"image_url,omitempty"`
	Description      string         `json:"description,omitempty"`
	Specs            map[string]any `json:"specs,omitempty"`
	PriceCents       int64          `json:"price_cents"`
	ListPriceCents   *int64         `json:"list_price_cents,omitempty"`
	PixPriceCents    *int64         `json:"pix_price_cents,omitempty"`
	Installments     *int           `json:"installments,omitempty"`
	InstallmentCents *int64         `json:"installment_cents,omitempty"`
	ShippingCents    *int64         `json:"shipping_cents,omitempty"`
	InStock          *bool          `json:"in_stock,omitempty"` // default: true
}

type ItemError struct {
	Index int    `json:"index"`
	SKU   string `json:"sku"`
	Error string `json:"error"`
}

type Result struct {
	Received     int         `json:"received"`
	Created      int         `json:"created"`
	Updated      int         `json:"updated"`
	PriceChanges int         `json:"price_changes"`
	NewProducts  int         `json:"new_products"`
	Rejected     []ItemError `json:"rejected"`
}

type Service struct {
	db *pgxpool.Pool
}

func NewService(db *pgxpool.Pool) *Service { return &Service{db: db} }

// HashKey é o formato salvo em stores.api_key_hash.
func HashKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// NewAPIKey gera uma chave aleatória (mostrada uma única vez ao criar a loja).
func NewAPIKey() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "ck_" + hex.EncodeToString(b), nil
}

// StoreByKey resolve a loja a partir da chave de ingestão.
func (s *Service) StoreByKey(ctx context.Context, key string) (int64, error) {
	if key == "" {
		return 0, ErrInvalidKey
	}
	var id int64
	err := s.db.QueryRow(ctx, `SELECT id FROM stores WHERE api_key_hash = $1 AND active`, HashKey(key)).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrInvalidKey
	}
	return id, err
}

func validate(in *OfferInput) error {
	in.SKU = strings.TrimSpace(in.SKU)
	in.Title = strings.TrimSpace(in.Title)
	in.EAN = strings.TrimSpace(in.EAN)
	in.Brand = strings.TrimSpace(in.Brand)
	switch {
	case in.SKU == "" || len(in.SKU) > 100:
		return errors.New("sku obrigatório (máx. 100)")
	case in.Title == "" || len(in.Title) > 300:
		return errors.New("title obrigatório (máx. 300)")
	case in.PriceCents <= 0:
		return errors.New("price_cents deve ser > 0")
	case in.PixPriceCents != nil && *in.PixPriceCents <= 0:
		return errors.New("pix_price_cents deve ser > 0")
	case in.ListPriceCents != nil && *in.ListPriceCents <= 0:
		return errors.New("list_price_cents deve ser > 0")
	case in.ShippingCents != nil && *in.ShippingCents < 0:
		return errors.New("shipping_cents deve ser >= 0")
	case in.Installments != nil && (*in.Installments < 1 || *in.Installments > 48):
		return errors.New("installments deve estar entre 1 e 48")
	case in.EAN != "" && !validEAN(in.EAN):
		return errors.New("ean inválido")
	}
	if err := validHTTPURL(in.URL); err != nil {
		return fmt.Errorf("url: %w", err)
	}
	if in.ImageURL != "" {
		if err := validHTTPURL(in.ImageURL); err != nil {
			return fmt.Errorf("image_url: %w", err)
		}
	}
	return nil
}

// validHTTPURL impede javascript:, data: etc., já que a URL vira redirect público.
func validHTTPURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("precisa ser http(s) absoluta")
	}
	return nil
}

// validEAN confere o dígito verificador GTIN-8/12/13/14.
func validEAN(code string) bool {
	n := len(code)
	if n != 8 && n != 12 && n != 13 && n != 14 {
		return false
	}
	sum := 0
	for i := 0; i < n-1; i++ {
		c := code[i]
		if c < '0' || c > '9' {
			return false
		}
		d := int(c - '0')
		// Peso 3 nas posições ímpares contando da direita (excluindo o verificador).
		if (n-1-i)%2 == 1 {
			d *= 3
		}
		sum += d
	}
	last := code[n-1]
	if last < '0' || last > '9' {
		return false
	}
	return (10-sum%10)%10 == int(last-'0')
}

// Import processa o lote numa única transação. Itens inválidos são rejeitados
// individualmente sem abortar o lote.
func (s *Service) Import(ctx context.Context, storeID int64, items []OfferInput) (Result, error) {
	res := Result{Received: len(items), Rejected: []ItemError{}}
	if len(items) == 0 || len(items) > MaxBatch {
		return res, ErrBatchSize
	}

	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		categories := map[string]*int64{}
		touched := map[int64]struct{}{}

		for i := range items {
			in := &items[i]
			if err := validate(in); err != nil {
				res.Rejected = append(res.Rejected, ItemError{Index: i, SKU: in.SKU, Error: err.Error()})
				continue
			}
			// Savepoint por item: um erro de banco num item não derruba o lote.
			sp, err := tx.Begin(ctx)
			if err != nil {
				return err
			}
			out, err := s.upsertOne(ctx, sp, storeID, in, categories)
			if err != nil {
				_ = sp.Rollback(ctx)
				res.Rejected = append(res.Rejected, ItemError{Index: i, SKU: in.SKU, Error: "erro ao gravar oferta"})
				continue
			}
			if err := sp.Commit(ctx); err != nil {
				return err
			}
			touched[out.productID] = struct{}{}
			if out.oldProductID != 0 && out.oldProductID != out.productID {
				touched[out.oldProductID] = struct{}{}
			}
			if out.created {
				res.Created++
			} else {
				res.Updated++
			}
			if out.priceChanged {
				res.PriceChanges++
			}
			if out.newProduct {
				res.NewProducts++
			}
		}

		ids := make([]int64, 0, len(touched))
		for id := range touched {
			ids = append(ids, id)
		}
		return RefreshProducts(ctx, tx, ids)
	})
	if err == nil && res.NewProducts > 0 {
		// Atualiza o dicionário da busca; CONCURRENTLY não bloqueia leituras.
		if _, rerr := s.db.Exec(ctx, `REFRESH MATERIALIZED VIEW CONCURRENTLY search_words`); rerr != nil {
			slog.WarnContext(ctx, "refresh search_words", "err", rerr)
		}
	}
	return res, err
}

type upsertOut struct {
	productID    int64
	oldProductID int64
	created      bool
	priceChanged bool
	newProduct   bool
}

func (s *Service) upsertOne(ctx context.Context, tx pgx.Tx, storeID int64, in *OfferInput, cats map[string]*int64) (upsertOut, error) {
	var out upsertOut

	// Oferta já existente desta loja (trava a linha para comparar o preço).
	var existingOfferID, oldPrice int64
	err := tx.QueryRow(ctx, `SELECT id, product_id, price_cents FROM offers WHERE store_id=$1 AND sku=$2 FOR UPDATE`,
		storeID, in.SKU).Scan(&existingOfferID, &out.oldProductID, &oldPrice)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}

	catID, err := s.categoryID(ctx, tx, in.Category, cats)
	if err != nil {
		return out, err
	}

	// Resolve o produto: EAN > oferta existente > novo produto.
	switch {
	case in.EAN != "":
		var inserted bool
		err = tx.QueryRow(ctx, `
			INSERT INTO products (slug, title, brand, ean, category_id, image_url, description, specs)
			VALUES ($1, $2, nullif($3,''), $4, $5, nullif($6,''), nullif($7,''), coalesce($8, '{}'::jsonb))
			ON CONFLICT (ean) DO UPDATE SET
				brand       = coalesce(products.brand, EXCLUDED.brand),
				category_id = coalesce(products.category_id, EXCLUDED.category_id),
				image_url   = coalesce(products.image_url, EXCLUDED.image_url),
				description = coalesce(products.description, EXCLUDED.description),
				specs       = EXCLUDED.specs || products.specs
			RETURNING id, (xmax = 0)`,
			newSlug(in.Title), in.Title, in.Brand, in.EAN, catID, in.ImageURL, in.Description, specsOrNil(in.Specs),
		).Scan(&out.productID, &inserted)
		out.newProduct = inserted
	case existingOfferID != 0:
		out.productID = out.oldProductID
	default:
		err = tx.QueryRow(ctx, `
			INSERT INTO products (slug, title, brand, category_id, image_url, description, specs)
			VALUES ($1, $2, nullif($3,''), $4, nullif($5,''), nullif($6,''), coalesce($7, '{}'::jsonb))
			RETURNING id`,
			newSlug(in.Title), in.Title, in.Brand, catID, in.ImageURL, in.Description, specsOrNil(in.Specs),
		).Scan(&out.productID)
		out.newProduct = true
	}
	if err != nil {
		return out, fmt.Errorf("resolve product: %w", err)
	}

	inStock := true
	if in.InStock != nil {
		inStock = *in.InStock
	}

	var offerID int64
	err = tx.QueryRow(ctx, `
		INSERT INTO offers (product_id, store_id, sku, title, url, price_cents, list_price_cents, pix_price_cents,
		                    installments, installment_cents, shipping_cents, in_stock, last_seen_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12, now())
		ON CONFLICT (store_id, sku) DO UPDATE SET
			product_id = EXCLUDED.product_id, title = EXCLUDED.title, url = EXCLUDED.url,
			price_cents = EXCLUDED.price_cents, list_price_cents = EXCLUDED.list_price_cents,
			pix_price_cents = EXCLUDED.pix_price_cents, installments = EXCLUDED.installments,
			installment_cents = EXCLUDED.installment_cents, shipping_cents = EXCLUDED.shipping_cents,
			in_stock = EXCLUDED.in_stock, last_seen_at = now(), updated_at = now()
		RETURNING id`,
		out.productID, storeID, in.SKU, in.Title, in.URL, in.PriceCents, in.ListPriceCents, in.PixPriceCents,
		in.Installments, in.InstallmentCents, in.ShippingCents, inStock,
	).Scan(&offerID)
	if err != nil {
		return out, fmt.Errorf("upsert offer: %w", err)
	}

	out.created = existingOfferID == 0
	out.priceChanged = out.created || oldPrice != in.PriceCents
	if out.priceChanged {
		if _, err := tx.Exec(ctx, `INSERT INTO price_history (offer_id, price_cents) VALUES ($1, $2)`, offerID, in.PriceCents); err != nil {
			return out, fmt.Errorf("price history: %w", err)
		}
	}
	if out.created {
		out.oldProductID = 0
	}
	return out, nil
}

func (s *Service) categoryID(ctx context.Context, tx pgx.Tx, slugStr string, cache map[string]*int64) (*int64, error) {
	if slugStr == "" {
		return nil, nil
	}
	if id, ok := cache[slugStr]; ok {
		return id, nil
	}
	var id int64
	err := tx.QueryRow(ctx, `SELECT id FROM categories WHERE slug = $1`, slugStr).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		cache[slugStr] = nil
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	cache[slugStr] = &id
	return &id, nil
}

// Execer é satisfeito por *pgxpool.Pool e pgx.Tx.
type Execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// RefreshProducts recalcula menor/maior preço e contagem de ofertas, e registra o menor preço do dia.
// Exportado porque o worker de ofertas vencidas também precisa dele.
func RefreshProducts(ctx context.Context, q Execer, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	if _, err := q.Exec(ctx, `
		UPDATE products p SET
			min_price_cents = s.min_price, max_price_cents = s.max_price,
			offers_count = s.cnt, updated_at = now()
		FROM (
			SELECT p2.id,
			       min(LEAST(o.price_cents, coalesce(o.pix_price_cents, o.price_cents))) AS min_price,
			       max(o.price_cents) AS max_price,
			       count(o.id) AS cnt
			FROM products p2
			LEFT JOIN offers o ON o.product_id = p2.id AND o.in_stock
			     AND EXISTS (SELECT 1 FROM stores s WHERE s.id = o.store_id AND s.active)
			WHERE p2.id = ANY($1)
			GROUP BY p2.id
		) s
		WHERE p.id = s.id`, ids); err != nil {
		return fmt.Errorf("refresh product stats: %w", err)
	}
	if _, err := q.Exec(ctx, `
		INSERT INTO product_price_daily (product_id, day, min_price_cents)
		SELECT id, current_date, min_price_cents FROM products
		WHERE id = ANY($1) AND min_price_cents IS NOT NULL
		ON CONFLICT (product_id, day) DO UPDATE
			SET min_price_cents = LEAST(product_price_daily.min_price_cents, EXCLUDED.min_price_cents)`, ids); err != nil {
		return fmt.Errorf("daily price: %w", err)
	}
	return nil
}

func newSlug(title string) string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	base := slug.Make(title)
	if base == "" {
		base = "produto"
	}
	return base + "-" + hex.EncodeToString(b)
}

// specsOrNil devolve nil "puro" (vira NULL no SQL) quando não há specs.
func specsOrNil(m map[string]any) any {
	if len(m) == 0 {
		return nil
	}
	return m
}

// ---------- administração de lojas ----------

type NewStore struct {
	Slug    string   `json:"slug"`
	Name    string   `json:"name"`
	Website string   `json:"website"`
	LogoURL string   `json:"logo_url,omitempty"`
	Rating  *float64 `json:"rating,omitempty"`
}

type CreatedStore struct {
	ID     int64  `json:"id"`
	Slug   string `json:"slug"`
	APIKey string `json:"api_key"` // exibida uma única vez
}

var ErrStoreExists = errors.New("loja já existe")

// ValidationError indica dado de entrada inválido (HTTP 422).
type ValidationError struct{ Msg string }

func (e ValidationError) Error() string { return e.Msg }

func (s *Service) CreateStore(ctx context.Context, in NewStore) (CreatedStore, error) {
	in.Slug = slug.Make(in.Slug)
	in.Name = strings.TrimSpace(in.Name)
	if in.Slug == "" || in.Name == "" {
		return CreatedStore{}, ValidationError{"slug e name são obrigatórios"}
	}
	if err := validHTTPURL(in.Website); err != nil {
		return CreatedStore{}, ValidationError{"website: " + err.Error()}
	}
	if in.Rating != nil && (*in.Rating < 0 || *in.Rating > 5) {
		return CreatedStore{}, ValidationError{"rating deve estar entre 0 e 5"}
	}
	key, err := NewAPIKey()
	if err != nil {
		return CreatedStore{}, err
	}
	out := CreatedStore{Slug: in.Slug, APIKey: key}
	err = s.db.QueryRow(ctx, `
		INSERT INTO stores (slug, name, website, logo_url, rating, api_key_hash)
		VALUES ($1,$2,$3,nullif($4,''),$5,$6) RETURNING id`,
		in.Slug, in.Name, in.Website, in.LogoURL, in.Rating, HashKey(key)).Scan(&out.ID)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return CreatedStore{}, ErrStoreExists
	}
	return out, err
}

type NewCategory struct {
	Slug       string `json:"slug"`
	Name       string `json:"name"`
	ParentSlug string `json:"parent_slug,omitempty"`
}

func (s *Service) CreateCategory(ctx context.Context, in NewCategory) (int64, error) {
	in.Slug = slug.Make(in.Slug)
	in.Name = strings.TrimSpace(in.Name)
	if in.Slug == "" || in.Name == "" {
		return 0, ValidationError{"slug e name são obrigatórios"}
	}
	var id int64
	err := s.db.QueryRow(ctx, `
		INSERT INTO categories (slug, name, parent_id)
		VALUES ($1, $2, (SELECT id FROM categories WHERE slug = nullif($3,'')))
		ON CONFLICT (slug) DO UPDATE SET name = EXCLUDED.name, parent_id = EXCLUDED.parent_id
		RETURNING id`, in.Slug, in.Name, in.ParentSlug).Scan(&id)
	return id, err
}
