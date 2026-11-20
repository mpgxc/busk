package crawler

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"

	"golang.org/x/net/html"

	"comparai/internal/ingest"
)

// ParseSitemap lê sitemap.xml ou sitemapindex.xml e devolve URLs (ou sub-sitemaps).
func ParseSitemap(body []byte) ([]string, error) {
	var doc struct {
		URLs     []string `xml:"url>loc"`
		Sitemaps []string `xml:"sitemap>loc"`
	}
	if err := xml.Unmarshal(body, &doc); err != nil {
		return nil, err
	}
	return append(doc.URLs, doc.Sitemaps...), nil
}

func looksLikeSitemap(body []byte) bool {
	head := body
	if len(head) > 512 {
		head = head[:512]
	}
	return bytes.Contains(head, []byte("<urlset")) || bytes.Contains(head, []byte("<sitemapindex"))
}

// ExtractLinks devolve os hrefs da página já resolvidos contra a URL base.
func ExtractLinks(body []byte, base *url.URL) []string {
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return nil
	}
	var out []string
	walk(doc, func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" {
			if href := attr(n, "href"); href != "" {
				if u, err := base.Parse(href); err == nil {
					out = append(out, u.String())
				}
			}
		}
	})
	return out
}

// ExtractProducts acha os objetos schema.org Product nos blocos JSON-LD da página.
// JSON-LD malformado é comum; esses blocos são ignorados em silêncio.
func ExtractProducts(body []byte) []map[string]any {
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return nil
	}
	var out []map[string]any
	walk(doc, func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "script" &&
			attr(n, "type") == "application/ld+json" && n.FirstChild != nil {
			var v any
			if json.Unmarshal([]byte(n.FirstChild.Data), &v) == nil {
				collectProducts(v, &out)
			}
		}
	})
	return out
}

func collectProducts(v any, out *[]map[string]any) {
	switch x := v.(type) {
	case []any:
		for _, e := range x {
			collectProducts(e, out)
		}
	case map[string]any:
		if g, ok := x["@graph"]; ok {
			collectProducts(g, out)
		}
		if hasType(x["@type"], "Product") {
			*out = append(*out, x)
		}
	}
}

func hasType(t any, want string) bool {
	switch v := t.(type) {
	case string:
		return v == want
	case []any:
		for _, e := range v {
			if s, ok := e.(string); ok && s == want {
				return true
			}
		}
	}
	return false
}

// ToOffers converte um Product do schema.org em ofertas no formato da ingestão.
// O que a ingestão rejeitaria (EAN inválido, URL não http, título longo) é tratado aqui.
func ToOffers(p map[string]any, page *url.URL) []ingest.OfferInput {
	name := clip(str(p["name"]), 300)
	if name == "" {
		return nil
	}
	sku := str(p["sku"])
	if sku == "" {
		sku = str(p["mpn"])
	}
	sku = clip(sku, 100)
	brand := clip(brandName(p["brand"]), 100)
	ean := gtin(p)
	image := imageURL(p["image"], page)
	desc := str(p["description"])

	var out []ingest.OfferInput
	for i, off := range flattenOffers(p["offers"]) {
		price, ok := parsePrice(off["price"])
		if !ok {
			continue
		}
		if cur := str(off["priceCurrency"]); cur != "" && cur != "BRL" {
			continue
		}
		offerURL := page.String()
		if raw := str(off["url"]); raw != "" {
			if u, err := page.Parse(raw); err == nil {
				offerURL = u.String()
			}
		}
		if !isHTTP(offerURL) {
			continue
		}
		id := sku
		if id == "" {
			id = shortHash(page.String())
		}
		if i > 0 {
			id = fmt.Sprintf("%s-%d", id, i)
		}
		stock := inStock(str(off["availability"]))
		out = append(out, ingest.OfferInput{
			SKU: id, EAN: ean, Title: name, Brand: brand, URL: offerURL,
			ImageURL: image, Description: desc, PriceCents: price, InStock: &stock,
		})
	}
	return out
}

// parsePrice converte para centavos. O schema.org usa ponto decimal ("1299.90");
// também aceitamos o formato brasileiro ("R$ 1.299,90").
func parsePrice(v any) (int64, bool) {
	var f float64
	switch x := v.(type) {
	case float64:
		f = x
	case string:
		s := strings.ReplaceAll(strings.TrimSpace(x), "R$", "")
		s = strings.ReplaceAll(strings.TrimSpace(s), " ", "")
		if strings.Contains(s, ",") {
			s = strings.ReplaceAll(s, ".", "")
			s = strings.Replace(s, ",", ".", 1)
		}
		p, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 0, false
		}
		f = p
	default:
		return 0, false
	}
	if math.IsNaN(f) || f <= 0 || f > 10_000_000 {
		return 0, false
	}
	return int64(math.Round(f * 100)), true
}

func gtin(p map[string]any) string {
	for _, k := range []string{"gtin13", "gtin14", "gtin12", "gtin8", "gtin", "ean"} {
		if code := digitsOnly(str(p[k])); ingest.ValidEAN(code) {
			return code
		}
	}
	return ""
}

func flattenOffers(v any) []map[string]any {
	switch x := v.(type) {
	case map[string]any:
		if _, hasPrice := x["price"]; hasPrice {
			return []map[string]any{x}
		}
		return flattenOffers(x["offers"]) // AggregateOffer: as ofertas individuais estão em "offers"
	case []any:
		var out []map[string]any
		for _, e := range x {
			out = append(out, flattenOffers(e)...)
		}
		return out
	}
	return nil
}

func inStock(availability string) bool {
	a := strings.ToLower(availability)
	if a == "" {
		return true // sem informação: assume disponível, como a ingestão faz
	}
	return !strings.Contains(a, "outofstock") && !strings.Contains(a, "soldout") && !strings.Contains(a, "discontinued")
}

func brandName(v any) string {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case map[string]any:
		return str(x["name"])
	case []any:
		if len(x) > 0 {
			return brandName(x[0])
		}
	}
	return ""
}

func imageURL(v any, base *url.URL) string {
	var raw string
	switch x := v.(type) {
	case string:
		raw = x
	case []any:
		if len(x) > 0 {
			raw, _ = x[0].(string)
		}
	case map[string]any:
		raw = str(x["url"])
	}
	if raw == "" {
		return ""
	}
	if u, err := base.Parse(raw); err == nil && isHTTP(u.String()) {
		return u.String()
	}
	return ""
}

func str(v any) string {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	}
	return ""
}

func digitsOnly(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func isHTTP(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}

func walk(n *html.Node, fn func(*html.Node)) {
	fn(n)
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walk(c, fn)
	}
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}
