package crawler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

const maxBodyBytes = 5 << 20 // 5 MB: página maior que isso não é de produto

// ErrDisallowed: o robots.txt do site proíbe esta URL para o nosso bot.
var ErrDisallowed = errors.New("bloqueado pelo robots.txt")

// Response é o resultado de uma requisição. Status diferente de 200 vem sem corpo.
type Response struct {
	Status      int
	URL         *url.URL // URL final, depois dos redirects
	ContentType string
	Body        []byte
	RetryAfter  time.Duration
}

// Fetcher faz as requisições respeitando robots.txt, Crawl-delay e um rate limit por host.
type Fetcher struct {
	client *http.Client
	ua     string
	robots *robotsCache

	mu     sync.Mutex
	limits map[string]*rate.Limiter // por host
}

func NewFetcher(userAgent string) *Fetcher {
	client := &http.Client{Timeout: 20 * time.Second}
	return &Fetcher{
		client: client,
		ua:     userAgent,
		robots: newRobotsCache(client, userAgent),
		limits: map[string]*rate.Limiter{},
	}
}

func (f *Fetcher) limiter(host string, rps float64) *rate.Limiter {
	f.mu.Lock()
	defer f.mu.Unlock()
	l, ok := f.limits[host]
	if !ok {
		l = rate.NewLimiter(rate.Limit(rps), 1)
		f.limits[host] = l
	}
	return l
}

// Fetch espera a vez no host, busca a URL e devolve a resposta.
func (f *Fetcher) Fetch(ctx context.Context, rawURL string, rps float64) (*Response, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	allowed, delay, err := f.robots.check(ctx, u)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, ErrDisallowed
	}

	lim := f.limiter(u.Host, rps)
	if delay > 0 && rate.Every(delay) < lim.Limit() {
		lim.SetLimit(rate.Every(delay)) // Crawl-delay mais lento que o nosso RPS vence
	}
	if err := lim.Wait(ctx); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", f.ua)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.5")

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	out := &Response{
		Status:      resp.StatusCode,
		URL:         resp.Request.URL,
		ContentType: resp.Header.Get("Content-Type"),
		RetryAfter:  parseRetryAfter(resp.Header.Get("Retry-After")),
	}
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return out, nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("lendo corpo: %w", err)
	}
	if len(body) > maxBodyBytes {
		return nil, fmt.Errorf("página maior que %d bytes", maxBodyBytes)
	}
	out.Body = body
	return out, nil
}

// parseRetryAfter aceita segundos ou data HTTP.
func parseRetryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	if s, err := strconv.Atoi(v); err == nil {
		return time.Duration(s) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		return time.Until(t)
	}
	return 0
}
