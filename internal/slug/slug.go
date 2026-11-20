// Package slug gera slugs ASCII para URLs a partir de texto em português.
package slug

import (
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// Make converte "Notebook Ação 15,6\"" em "notebook-acao-15-6".
func Make(s string) string {
	t := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
	ascii, _, err := transform.String(t, s)
	if err != nil {
		ascii = s
	}
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(ascii) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		default:
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	out := strings.TrimSuffix(b.String(), "-")
	if len(out) > 80 {
		out = strings.TrimSuffix(out[:80], "-")
	}
	return out
}
