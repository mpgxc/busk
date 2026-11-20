// Package alerts mantém favoritos, alertas de preço, notificações e o worker
// que dispara alertas quando o menor preço atinge o alvo do usuário.
package alerts

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"comparai/internal/catalog"
	"comparai/internal/ingest"
)

var ErrNotFound = errors.New("not found")

type Alert struct {
	ID               int64      `json:"id"`
	ProductSlug      string     `json:"product_slug"`
	ProductTitle     string     `json:"product_title"`
	ImageURL         *string    `json:"image_url"`
	TargetPriceCents int64      `json:"target_price_cents"`
	CurrentPrice     *int64     `json:"current_price_cents"`
	Active           bool       `json:"active"`
	TriggeredAt      *time.Time `json:"triggered_at"`
	CreatedAt        time.Time  `json:"created_at"`
}

type Notification struct {
	ID          int64      `json:"id"`
	Kind        string     `json:"kind"`
	Message     string     `json:"message"`
	ProductSlug *string    `json:"product_slug"`
	ReadAt      *time.Time `json:"read_at"`
	CreatedAt   time.Time  `json:"created_at"`
}

// Notifier entrega a notificação fora do app (email, push, WhatsApp...).
type Notifier interface {
	Notify(ctx context.Context, userID int64, message string) error
}

// LogNotifier só registra em log — troque por um provedor real.
type LogNotifier struct{}

func (LogNotifier) Notify(_ context.Context, userID int64, message string) error {
	slog.Info("notification", "user_id", userID, "message", message)
	return nil
}

type Service struct {
	db       *pgxpool.Pool
	notifier Notifier
}

func NewService(db *pgxpool.Pool, n Notifier) *Service { return &Service{db: db, notifier: n} }

// ---------- favoritos ----------

func (s *Service) AddFavorite(ctx context.Context, userID, productID int64) error {
	_, err := s.db.Exec(ctx, `INSERT INTO favorites (user_id, product_id) VALUES ($1,$2) ON CONFLICT DO NOTHING`, userID, productID)
	return err
}

func (s *Service) RemoveFavorite(ctx context.Context, userID, productID int64) error {
	_, err := s.db.Exec(ctx, `DELETE FROM favorites WHERE user_id=$1 AND product_id=$2`, userID, productID)
	return err
}

func (s *Service) Favorites(ctx context.Context, userID int64) ([]catalog.ProductSummary, error) {
	rows, err := s.db.Query(ctx, `
		SELECT p.id, p.slug, p.title, p.brand, p.image_url, c.slug, p.min_price_cents, p.max_price_cents, p.offers_count
		FROM favorites f JOIN products p ON p.id = f.product_id
		LEFT JOIN categories c ON c.id = p.category_id
		WHERE f.user_id = $1 ORDER BY f.created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []catalog.ProductSummary{}
	for rows.Next() {
		var p catalog.ProductSummary
		if err := rows.Scan(&p.ID, &p.Slug, &p.Title, &p.Brand, &p.ImageURL, &p.CategorySlug,
			&p.MinPriceCents, &p.MaxPriceCents, &p.OffersCount); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ---------- alertas ----------

// UpsertAlert cria ou reativa o alerta do usuário para o produto.
func (s *Service) UpsertAlert(ctx context.Context, userID, productID, targetCents int64) (int64, error) {
	if targetCents <= 0 {
		return 0, errors.New("target_price_cents deve ser > 0")
	}
	var id int64
	err := s.db.QueryRow(ctx, `
		INSERT INTO price_alerts (user_id, product_id, target_price_cents) VALUES ($1,$2,$3)
		ON CONFLICT (user_id, product_id) DO UPDATE
			SET target_price_cents = EXCLUDED.target_price_cents, active = true, triggered_at = NULL
		RETURNING id`, userID, productID, targetCents).Scan(&id)
	return id, err
}

func (s *Service) DeleteAlert(ctx context.Context, userID, alertID int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM price_alerts WHERE id=$1 AND user_id=$2`, alertID, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Service) Alerts(ctx context.Context, userID int64) ([]Alert, error) {
	rows, err := s.db.Query(ctx, `
		SELECT a.id, p.slug, p.title, p.image_url, a.target_price_cents, p.min_price_cents,
		       a.active, a.triggered_at, a.created_at
		FROM price_alerts a JOIN products p ON p.id = a.product_id
		WHERE a.user_id = $1 ORDER BY a.created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Alert{}
	for rows.Next() {
		var a Alert
		if err := rows.Scan(&a.ID, &a.ProductSlug, &a.ProductTitle, &a.ImageURL, &a.TargetPriceCents,
			&a.CurrentPrice, &a.Active, &a.TriggeredAt, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ---------- notificações ----------

func (s *Service) Notifications(ctx context.Context, userID int64, limit int) ([]Notification, error) {
	rows, err := s.db.Query(ctx, `
		SELECT n.id, n.kind, n.message, p.slug, n.read_at, n.created_at
		FROM notifications n LEFT JOIN products p ON p.id = n.product_id
		WHERE n.user_id = $1 ORDER BY n.created_at DESC LIMIT $2`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Notification{}
	for rows.Next() {
		var n Notification
		if err := rows.Scan(&n.ID, &n.Kind, &n.Message, &n.ProductSlug, &n.ReadAt, &n.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *Service) MarkAllRead(ctx context.Context, userID int64) error {
	_, err := s.db.Exec(ctx, `UPDATE notifications SET read_at = now() WHERE user_id=$1 AND read_at IS NULL`, userID)
	return err
}

// ---------- worker ----------

// CheckAlerts dispara os alertas cujo preço alvo foi atingido. Usa SKIP LOCKED,
// então várias instâncias podem rodar o worker em paralelo sem duplicar avisos.
func (s *Service) CheckAlerts(ctx context.Context) (int, error) {
	type fired struct {
		userID  int64
		message string
	}
	var sent []fired
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			WITH due AS (
				SELECT a.id, a.user_id, a.product_id, a.target_price_cents, p.min_price_cents, p.title
				FROM price_alerts a JOIN products p ON p.id = a.product_id
				WHERE a.active AND p.min_price_cents IS NOT NULL AND p.min_price_cents <= a.target_price_cents
				ORDER BY a.id
				LIMIT 500
				FOR UPDATE OF a SKIP LOCKED
			), upd AS (
				UPDATE price_alerts a SET active = false, triggered_at = now()
				FROM due WHERE a.id = due.id
			)
			INSERT INTO notifications (user_id, product_id, kind, message)
			SELECT user_id, product_id, 'price_alert',
			       -- translate() troca , e . para o formato brasileiro (1.299,90)
			       format('Baixou! %s está por R$ %s (seu alvo: R$ %s)', title,
			              translate(to_char(min_price_cents / 100.0, 'FM999,999,990.00'), ',.', '.,'),
			              translate(to_char(target_price_cents / 100.0, 'FM999,999,990.00'), ',.', '.,'))
			FROM due
			RETURNING user_id, message`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var f fired
			if err := rows.Scan(&f.userID, &f.message); err != nil {
				return err
			}
			sent = append(sent, f)
		}
		return rows.Err()
	})
	if err != nil {
		return 0, fmt.Errorf("check alerts: %w", err)
	}
	// Entrega externa fora da transação: a notificação in-app já está salva.
	for _, f := range sent {
		if err := s.notifier.Notify(ctx, f.userID, f.message); err != nil {
			slog.Warn("notifier failed", "user_id", f.userID, "err", err)
		}
	}
	return len(sent), nil
}

// ExpireStaleOffers marca como fora de estoque ofertas que a loja parou de enviar.
func (s *Service) ExpireStaleOffers(ctx context.Context, olderThan time.Duration) (int, error) {
	var n int
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			UPDATE offers SET in_stock = false, updated_at = now()
			WHERE in_stock AND last_seen_at < now() - make_interval(secs => $1)
			RETURNING product_id`, olderThan.Seconds())
		if err != nil {
			return err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
		if err != nil {
			return err
		}
		n = len(ids)
		return ingest.RefreshProducts(ctx, tx, ids)
	})
	return n, err
}

// Run executa o worker até o contexto ser cancelado.
func (s *Service) Run(ctx context.Context, every, staleAfter time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if n, err := s.CheckAlerts(ctx); err != nil {
			slog.Error("alerts worker", "err", err)
		} else if n > 0 {
			slog.Info("alerts fired", "count", n)
		}
		if n, err := s.ExpireStaleOffers(ctx, staleAfter); err != nil {
			slog.Error("stale offers worker", "err", err)
		} else if n > 0 {
			slog.Info("stale offers expired", "count", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
