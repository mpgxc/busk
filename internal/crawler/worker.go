package crawler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"comparai/internal/ingest"
)

// flushSize: ofertas acumuladas por loja antes de gravar em lote.
const flushSize = 100

// outcome é o desfecho de uma URL, gravado na fila.
type outcome struct {
	Status   string // ok | error | gone | skipped
	NextAt   time.Time
	Attempts int
	Error    string
}

// Worker consome a fila: busca, extrai produtos e ofertas, descobre links e grava na ingestão.
type Worker struct {
	front   *Frontier
	fetch   *Fetcher
	ing     *ingest.Service
	pending map[int64][]ingest.OfferInput // por loja
	regexes map[string]*regexp.Regexp
}

func NewWorker(db *pgxpool.Pool, userAgent string, ing *ingest.Service) *Worker {
	return &Worker{
		front:   &Frontier{db: db},
		fetch:   NewFetcher(userAgent),
		ing:     ing,
		pending: map[int64][]ingest.OfferInput{},
		regexes: map[string]*regexp.Regexp{},
	}
}

// Drain processa tudo que está vencido e para quando a fila não tem mais nada elegível.
func (w *Worker) Drain(ctx context.Context) (int, error) {
	processed := 0
	for ctx.Err() == nil {
		jobs, err := w.front.Claim(ctx, 5)
		if err != nil {
			return processed, err
		}
		if len(jobs) == 0 {
			break
		}
		for _, j := range jobs {
			w.process(ctx, j)
			processed++
		}
	}
	return processed, w.flushAll(ctx)
}

// Run mantém o worker ativo: drena a fila e espera `idle` antes de olhar de novo.
func (w *Worker) Run(ctx context.Context, idle time.Duration) error {
	for {
		n, err := w.Drain(ctx)
		if err != nil {
			slog.Error("crawler: drenar fila", "err", err)
		} else if n > 0 {
			slog.Info("crawler: páginas processadas", "count", n)
		}
		select {
		case <-ctx.Done():
			return w.flushAll(context.Background())
		case <-time.After(idle):
		}
	}
}

func (w *Worker) process(ctx context.Context, j Job) {
	src := j.Source
	next := time.Now().Add(time.Duration(src.RefreshMinutes) * time.Minute)

	resp, err := w.fetch.Fetch(ctx, j.URL, src.RPS)
	if errors.Is(err, ErrDisallowed) {
		w.done(ctx, j, outcome{Status: "skipped", NextAt: next, Error: err.Error()})
		return
	}
	if err != nil {
		w.retry(ctx, j, 0, err.Error())
		return
	}

	switch {
	case resp.Status == http.StatusNotFound || resp.Status == http.StatusGone:
		// Produto saiu do ar: as ofertas param de ser vistas e expiram pelo STALE_OFFER_AFTER.
		w.done(ctx, j, outcome{Status: "gone", NextAt: next, Error: fmt.Sprintf("HTTP %d", resp.Status)})
		return
	case resp.Status == http.StatusTooManyRequests || resp.Status >= 500:
		w.retry(ctx, j, resp.RetryAfter, fmt.Sprintf("HTTP %d", resp.Status))
		return
	case resp.Status != http.StatusOK:
		w.retry(ctx, j, 0, fmt.Sprintf("HTTP %d", resp.Status))
		return
	}

	if resp.URL.Host != src.Host {
		w.done(ctx, j, outcome{Status: "skipped", NextAt: next, Error: "redirecionou para outro host"})
		return
	}

	switch {
	case looksLikeSitemap(resp.Body):
		urls, err := ParseSitemap(resp.Body)
		if err != nil {
			w.retry(ctx, j, 0, "sitemap inválido: "+err.Error())
			return
		}
		if _, err := w.front.Enqueue(ctx, src, 0, w.filter(urls, src)); err != nil {
			slog.Error("crawler: enfileirar sitemap", "err", err)
		}
	case strings.Contains(resp.ContentType, "html"):
		for _, p := range ExtractProducts(resp.Body) {
			w.add(ctx, src.StoreID, ToOffers(p, resp.URL))
		}
		if j.Depth < src.MaxDepth {
			links := w.filter(ExtractLinks(resp.Body, resp.URL), src)
			if _, err := w.front.Enqueue(ctx, src, j.Depth+1, links); err != nil {
				slog.Error("crawler: enfileirar links", "err", err)
			}
		}
	default:
		w.done(ctx, j, outcome{Status: "skipped", NextAt: next, Error: "tipo não suportado: " + resp.ContentType})
		return
	}
	w.done(ctx, j, outcome{Status: "ok", NextAt: next})
}

// filter mantém só links do mesmo host, sem fragmento, que casam com include_regex.
func (w *Worker) filter(links []string, src Source) []string {
	var re *regexp.Regexp
	if src.IncludeRegex != "" {
		re = w.regex(src.IncludeRegex)
		if re == nil {
			return nil // regex inválida: não seguimos nada
		}
	}
	seen := make(map[string]bool, len(links))
	out := make([]string, 0, len(links))
	for _, raw := range links {
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host != src.Host {
			continue
		}
		u.Fragment = ""
		if re != nil && !re.MatchString(u.RequestURI()) {
			continue
		}
		s := u.String()
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func (w *Worker) regex(pattern string) *regexp.Regexp {
	if re, ok := w.regexes[pattern]; ok {
		return re
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		slog.Warn("crawler: include_regex inválida", "pattern", pattern, "err", err)
		re = nil
	}
	w.regexes[pattern] = re
	return re
}

func (w *Worker) add(ctx context.Context, storeID int64, offers []ingest.OfferInput) {
	if len(offers) == 0 {
		return
	}
	w.pending[storeID] = append(w.pending[storeID], offers...)
	if len(w.pending[storeID]) >= flushSize {
		if err := w.flush(ctx, storeID); err != nil {
			slog.Error("crawler: gravar ofertas", "store_id", storeID, "err", err)
		}
	}
}

func (w *Worker) flush(ctx context.Context, storeID int64) error {
	items := w.pending[storeID]
	delete(w.pending, storeID)
	if len(items) == 0 {
		return nil
	}
	res, err := w.ing.Import(ctx, storeID, items)
	if err != nil {
		return err
	}
	slog.Info("crawler: ofertas importadas", "store_id", storeID,
		"recebidas", res.Received, "novas", res.Created, "atualizadas", res.Updated,
		"mudancas_preco", res.PriceChanges, "rejeitadas", len(res.Rejected))
	return nil
}

func (w *Worker) flushAll(ctx context.Context) error {
	var first error
	for storeID := range w.pending {
		if err := w.flush(ctx, storeID); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (w *Worker) done(ctx context.Context, j Job, o outcome) {
	if err := w.front.Done(ctx, j.ID, o); err != nil {
		slog.Error("crawler: gravar resultado", "id", j.ID, "err", err)
	}
}

// retry agenda nova tentativa com backoff exponencial (1, 2, 4… min, teto de 24h).
// Se o site mandou Retry-After maior, respeitamos o site.
func (w *Worker) retry(ctx context.Context, j Job, retryAfter time.Duration, msg string) {
	attempts := j.Attempts + 1
	wait := time.Minute << min(attempts-1, 10)
	if retryAfter > wait {
		wait = retryAfter
	}
	w.done(ctx, j, outcome{
		Status:   "error",
		NextAt:   time.Now().Add(min(wait, 24*time.Hour)),
		Attempts: attempts,
		Error:    msg,
	})
}
