package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5/middleware"
)

type errorBody struct {
	Error     string `json:"error"`
	RequestID string `json:"request_id,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, r *http.Request, status int, msg string) {
	writeJSON(w, status, errorBody{Error: msg, RequestID: middleware.GetReqID(r.Context())})
}

// internalError loga o erro real e devolve mensagem genérica.
func internalError(w http.ResponseWriter, r *http.Request, err error) {
	slog.ErrorContext(r.Context(), "internal error", "err", err, "path", r.URL.Path,
		"request_id", middleware.GetReqID(r.Context()))
	writeError(w, r, http.StatusInternalServerError, "erro interno")
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any, maxBytes int64) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return errors.New("corpo da requisição muito grande")
		}
		return errors.New("JSON inválido: " + err.Error())
	}
	return nil
}

func queryInt(r *http.Request, key string, def, min, max int) int {
	v, err := strconv.Atoi(r.URL.Query().Get(key))
	if err != nil {
		return def
	}
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

// queryCents aceita "1999.90", "1999,90" ou "1999" (reais) e devolve centavos.
func queryCents(r *http.Request, key string) int64 {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return 0
	}
	raw = strings.ReplaceAll(raw, ",", ".")
	intPart, frac, _ := strings.Cut(raw, ".")
	reais, err := strconv.ParseInt(intPart, 10, 64)
	if err != nil || reais < 0 || reais > 100_000_000 {
		return 0
	}
	frac = (frac + "00")[:2]
	cents, err := strconv.ParseInt(frac, 10, 64)
	if err != nil {
		return 0
	}
	return reais*100 + cents
}

// queryList aceita ?brand=a&brand=b e ?brand=a,b.
func queryList(r *http.Request, key string) []string {
	var out []string
	for _, v := range r.URL.Query()[key] {
		for _, p := range strings.Split(v, ",") {
			if p = strings.TrimSpace(p); p != "" && len(out) < 20 {
				out = append(out, p)
			}
		}
	}
	return out
}
