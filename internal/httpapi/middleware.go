package httpapi

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"golang.org/x/time/rate"
)

type ctxKey int

const userIDKey ctxKey = 1

func userID(ctx context.Context) (int64, bool) {
	id, ok := ctx.Value(userIDKey).(int64)
	return id, ok
}

// requestLogger registra cada request em JSON via slog.
func requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		if r.URL.Path == "/healthz" {
			return
		}
		slog.Info("http",
			"method", r.Method, "path", r.URL.Path, "status", ww.Status(),
			"bytes", ww.BytesWritten(), "duration_ms", time.Since(start).Milliseconds(),
			"request_id", middleware.GetReqID(r.Context()))
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		next.ServeHTTP(w, r)
	})
}

func cors(origins string) func(http.Handler) http.Handler {
	allowed := map[string]bool{}
	for _, o := range strings.Split(origins, ",") {
		allowed[strings.TrimSpace(o)] = true
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin != "" && (allowed["*"] || allowed[origin]) {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Vary", "Origin")
				w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Store-Key")
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Max-Age", "600")
			}
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ipRateLimiter é um token bucket por IP em memória. Para várias réplicas,
// troque por Redis ou faça o rate limit no gateway.
type ipRateLimiter struct {
	mu      sync.Mutex
	clients map[string]*client
	rps     rate.Limit
	burst   int
}

type client struct {
	lim      *rate.Limiter
	lastSeen time.Time
}

func newIPRateLimiter(ctx context.Context, rps float64, burst int) *ipRateLimiter {
	l := &ipRateLimiter{clients: map[string]*client{}, rps: rate.Limit(rps), burst: burst}
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				l.mu.Lock()
				for ip, c := range l.clients {
					if time.Since(c.lastSeen) > 3*time.Minute {
						delete(l.clients, ip)
					}
				}
				l.mu.Unlock()
			}
		}
	}()
	return l
}

func (l *ipRateLimiter) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			ip = r.RemoteAddr
		}
		l.mu.Lock()
		c, ok := l.clients[ip]
		if !ok {
			c = &client{lim: rate.NewLimiter(l.rps, l.burst)}
			l.clients[ip] = c
		}
		c.lastSeen = time.Now()
		allowed := c.lim.Allow()
		l.mu.Unlock()
		if !allowed {
			w.Header().Set("Retry-After", "1")
			writeError(w, r, http.StatusTooManyRequests, "muitas requisições, tente novamente em instantes")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if t, ok := strings.CutPrefix(h, "Bearer "); ok {
		return strings.TrimSpace(t)
	}
	return ""
}

// optionalAuth injeta o user id se houver token válido; nunca bloqueia.
func (s *Server) optionalAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tok := bearerToken(r); tok != "" {
			if id, err := s.auth.ParseToken(tok); err == nil {
				r = r.WithContext(context.WithValue(r.Context(), userIDKey, id))
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := s.auth.ParseToken(bearerToken(r))
		if err != nil {
			writeError(w, r, http.StatusUnauthorized, "autenticação necessária")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userIDKey, id)))
	})
}

func (s *Server) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !constantTimeEqual(bearerToken(r), s.cfg.AdminToken) {
			writeError(w, r, http.StatusUnauthorized, "token de admin inválido")
			return
		}
		next.ServeHTTP(w, r)
	})
}
