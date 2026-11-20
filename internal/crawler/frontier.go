package crawler

import (
	"context"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Source é um host de loja a ser rastreado, com seus limites de politeness.
type Source struct {
	ID             int64
	StoreID        int64
	Host           string
	RPS            float64
	RefreshMinutes int
	MaxDepth       int
	MaxURLs        int
	IncludeRegex   string
}

// Job é uma URL reservada por um worker.
type Job struct {
	ID       int64
	URL      string
	Depth    int
	Attempts int
	Source   Source
}

// leaseDuration: se o worker cair no meio, a URL volta para a fila depois disso.
const leaseDuration = 5 * time.Minute

// Frontier é a fila de URLs no Postgres. Vários workers podem consumir sem duplicar (SKIP LOCKED).
type Frontier struct{ db *pgxpool.Pool }

// AddSource cria ou atualiza a fonte e enfileira a URL inicial (sitemap ou página de listagem).
func AddSource(ctx context.Context, db *pgxpool.Pool, src Source, seed string) error {
	if src.IncludeRegex != "" {
		if _, err := regexp.Compile(src.IncludeRegex); err != nil {
			return err
		}
	}
	err := db.QueryRow(ctx, `
		INSERT INTO crawl_sources (store_id, host, rps, refresh_minutes, max_depth, max_urls, include_regex)
		VALUES ($1, $2, $3, $4, $5, $6, nullif($7, ''))
		ON CONFLICT (store_id, host) DO UPDATE SET
			rps = EXCLUDED.rps, refresh_minutes = EXCLUDED.refresh_minutes, max_depth = EXCLUDED.max_depth,
			max_urls = EXCLUDED.max_urls, include_regex = EXCLUDED.include_regex, enabled = true
		RETURNING id`,
		src.StoreID, src.Host, src.RPS, src.RefreshMinutes, src.MaxDepth, src.MaxURLs, src.IncludeRegex,
	).Scan(&src.ID)
	if err != nil {
		return err
	}
	_, err = (&Frontier{db: db}).Enqueue(ctx, src, 0, []string{seed})
	return err
}

// Claim reserva até `limit` URLs vencidas, de fontes habilitadas.
func (f *Frontier) Claim(ctx context.Context, limit int) ([]Job, error) {
	rows, err := f.db.Query(ctx, `
		WITH due AS (
			SELECT u.id
			FROM crawl_urls u JOIN crawl_sources s ON s.id = u.source_id
			WHERE s.enabled
			  AND u.next_at <= now()
			  AND (u.locked_until IS NULL OR u.locked_until < now())
			ORDER BY u.next_at
			LIMIT $1
			FOR UPDATE OF u SKIP LOCKED
		)
		UPDATE crawl_urls u
		SET locked_until = now() + make_interval(secs => $2)
		FROM due, crawl_sources s
		WHERE u.id = due.id AND s.id = u.source_id
		RETURNING u.id, u.url, u.depth, u.attempts,
		          s.id, s.store_id, s.host, s.rps::float8, s.refresh_minutes,
		          s.max_depth, s.max_urls, coalesce(s.include_regex, '')`,
		limit, leaseDuration.Seconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var jobs []Job
	for rows.Next() {
		var j Job
		if err := rows.Scan(&j.ID, &j.URL, &j.Depth, &j.Attempts,
			&j.Source.ID, &j.Source.StoreID, &j.Source.Host, &j.Source.RPS, &j.Source.RefreshMinutes,
			&j.Source.MaxDepth, &j.Source.MaxURLs, &j.Source.IncludeRegex); err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

// Enqueue adiciona URLs novas (as já existentes mantêm o agendamento). Respeita max_urls.
func (f *Frontier) Enqueue(ctx context.Context, src Source, depth int, urls []string) (int, error) {
	if len(urls) == 0 {
		return 0, nil
	}
	var count int
	if err := f.db.QueryRow(ctx, `SELECT count(*) FROM crawl_urls WHERE source_id = $1`, src.ID).Scan(&count); err != nil {
		return 0, err
	}
	room := src.MaxURLs - count
	if room <= 0 {
		return 0, nil
	}
	if len(urls) > room {
		urls = urls[:room]
	}
	tag, err := f.db.Exec(ctx, `
		INSERT INTO crawl_urls (source_id, url, depth)
		SELECT $1, u, $2 FROM unnest($3::text[]) AS u
		ON CONFLICT (source_id, url) DO NOTHING`, src.ID, depth, urls)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// Done grava o resultado e agenda a próxima coleta da URL.
func (f *Frontier) Done(ctx context.Context, id int64, o outcome) error {
	_, err := f.db.Exec(ctx, `
		UPDATE crawl_urls
		SET status = $2, next_at = $3, attempts = $4, last_error = nullif($5, ''),
		    last_crawled_at = now(), locked_until = NULL
		WHERE id = $1`, id, o.Status, o.NextAt, o.Attempts, o.Error)
	return err
}
