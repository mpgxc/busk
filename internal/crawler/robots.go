package crawler

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/temoto/robotstxt"
)

// botToken é o nome que casa com o grupo "User-agent" do robots.txt.
const botToken = "ComparaiBot"

type robotsCache struct {
	client *http.Client
	ua     string

	mu   sync.Mutex
	data map[string]robotsEntry // chave: scheme://host
}

type robotsEntry struct {
	rules   *robotstxt.RobotsData
	expires time.Time
}

func newRobotsCache(client *http.Client, ua string) *robotsCache {
	return &robotsCache{client: client, ua: ua, data: map[string]robotsEntry{}}
}

// check diz se a URL pode ser buscada e devolve o Crawl-delay pedido pelo site.
// Se o robots.txt não puder ser lido por erro de rede, devolve erro: a fonte espera, não adivinha.
func (c *robotsCache) check(ctx context.Context, u *url.URL) (bool, time.Duration, error) {
	rules, err := c.rulesFor(ctx, u)
	if err != nil {
		return false, 0, err
	}
	grp := rules.FindGroup(botToken)
	return grp.Test(u.RequestURI()), grp.CrawlDelay, nil
}

func (c *robotsCache) rulesFor(ctx context.Context, u *url.URL) (*robotstxt.RobotsData, error) {
	key := u.Scheme + "://" + u.Host

	c.mu.Lock()
	e, ok := c.data[key]
	c.mu.Unlock()
	if ok && time.Now().Before(e.expires) {
		return e.rules, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, key+"/robots.txt", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.ua)
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("robots.txt: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 512<<10))
	if err != nil {
		return nil, fmt.Errorf("robots.txt: %w", err)
	}
	// Convenção do Google: 4xx = tudo liberado; 5xx = tudo bloqueado (site instável, não insista).
	rules, err := robotstxt.FromStatusAndBytes(resp.StatusCode, body)
	if err != nil {
		return nil, fmt.Errorf("robots.txt: %w", err)
	}

	c.mu.Lock()
	c.data[key] = robotsEntry{rules: rules, expires: time.Now().Add(time.Hour)}
	c.mu.Unlock()
	return rules, nil
}
