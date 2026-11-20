// Package httpapi expõe a API REST e serve o frontend embutido.
package httpapi

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"

	"comparai/internal/alerts"
	"comparai/internal/auth"
	"comparai/internal/catalog"
	"comparai/internal/config"
	"comparai/internal/ingest"
)

type Server struct {
	cfg     config.Config
	db      *pgxpool.Pool
	catalog *catalog.Repo
	ingest  *ingest.Service
	auth    *auth.Service
	alerts  *alerts.Service
	static  fs.FS
}

func New(cfg config.Config, db *pgxpool.Pool, cat *catalog.Repo, ing *ingest.Service,
	au *auth.Service, al *alerts.Service, static fs.FS) *Server {
	return &Server{cfg: cfg, db: db, catalog: cat, ingest: ing, auth: au, alerts: al, static: static}
}

func (s *Server) Routes(ctx context.Context) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	if s.cfg.TrustProxy {
		// Só confie em X-Forwarded-For atrás de um proxy/load balancer seu.
		r.Use(middleware.RealIP)
	}
	r.Use(requestLogger, middleware.Recoverer, securityHeaders, cors(s.cfg.CORSOrigins))
	r.Use(newIPRateLimiter(ctx, s.cfg.RateLimitPerSecond, s.cfg.RateLimitBurst).middleware)
	r.Use(middleware.Timeout(15 * time.Second))

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) }) //nolint:errcheck
	r.Get("/readyz", s.ready)
	r.With(s.optionalAuth).Get("/r/{offerID}", s.redirectToStore)

	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/search", s.search)
		r.Get("/suggest", s.suggest)
		r.Get("/products/{slug}", s.product)
		r.Get("/products/{slug}/history", s.priceHistory)
		r.Get("/categories", s.categories)
		r.Get("/stores", s.stores)
		r.Get("/deals", s.deals)

		r.Post("/auth/register", s.register)
		r.Post("/auth/login", s.login)

		r.Group(func(r chi.Router) {
			r.Use(s.requireAuth)
			r.Get("/me", s.me)
			r.Get("/me/favorites", s.listFavorites)
			r.Put("/me/favorites/{slug}", s.addFavorite)
			r.Delete("/me/favorites/{slug}", s.removeFavorite)
			r.Get("/me/alerts", s.listAlerts)
			r.Post("/me/alerts", s.createAlert)
			r.Delete("/me/alerts/{id}", s.deleteAlert)
			r.Get("/me/notifications", s.listNotifications)
			r.Post("/me/notifications/read", s.readNotifications)
		})

		// Integração com lojas: autenticada pela chave da loja.
		r.Post("/ingest/offers", s.ingestOffers)

		r.Group(func(r chi.Router) {
			r.Use(s.requireAdmin)
			r.Post("/admin/stores", s.createStore)
			r.Post("/admin/categories", s.createCategory)
		})

		r.NotFound(func(w http.ResponseWriter, r *http.Request) {
			writeError(w, r, http.StatusNotFound, "rota não encontrada")
		})
	})

	if s.static != nil {
		r.Handle("/*", spaHandler(s.static))
	}
	return r
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.db.Ping(ctx); err != nil {
		writeError(w, r, http.StatusServiceUnavailable, "database indisponível")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// ---------- catálogo ----------

func cachePublic(w http.ResponseWriter, seconds int) {
	w.Header().Set("Cache-Control", "public, max-age="+strconv.Itoa(seconds))
}

func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	params := catalog.SearchParams{
		Query:         strings.TrimSpace(q.Get("q")),
		CategorySlug:  q.Get("category"),
		Brands:        queryList(r, "brand"),
		StoreSlugs:    queryList(r, "store"),
		MinPriceCents: queryCents(r, "min_price"),
		MaxPriceCents: queryCents(r, "max_price"),
		Sort:          catalog.Sort(q.Get("sort")),
		Page:          queryInt(r, "page", 1, 1, 500),
		PerPage:       queryInt(r, "per_page", 24, 1, 60),
	}
	if len(params.Query) > 200 {
		writeError(w, r, http.StatusBadRequest, "busca muito longa")
		return
	}
	res, err := s.catalog.Search(r.Context(), params)
	if err != nil {
		internalError(w, r, err)
		return
	}
	cachePublic(w, 30)
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) suggest(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len([]rune(q)) < 2 || len(q) > 100 {
		writeJSON(w, http.StatusOK, []catalog.ProductSummary{})
		return
	}
	items, err := s.catalog.Suggest(r.Context(), q, queryInt(r, "limit", 8, 1, 15))
	if err != nil {
		internalError(w, r, err)
		return
	}
	cachePublic(w, 60)
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) product(w http.ResponseWriter, r *http.Request) {
	p, err := s.catalog.ProductBySlug(r.Context(), chi.URLParam(r, "slug"))
	if errors.Is(err, catalog.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, "produto não encontrado")
		return
	}
	if err != nil {
		internalError(w, r, err)
		return
	}
	cachePublic(w, 30)
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) priceHistory(w http.ResponseWriter, r *http.Request) {
	pts, err := s.catalog.PriceHistory(r.Context(), chi.URLParam(r, "slug"), queryInt(r, "days", 90, 7, 365))
	if err != nil {
		internalError(w, r, err)
		return
	}
	cachePublic(w, 300)
	writeJSON(w, http.StatusOK, pts)
}

func (s *Server) categories(w http.ResponseWriter, r *http.Request) {
	tree, err := s.catalog.CategoryTree(r.Context())
	if err != nil {
		internalError(w, r, err)
		return
	}
	cachePublic(w, 300)
	writeJSON(w, http.StatusOK, tree)
}

func (s *Server) stores(w http.ResponseWriter, r *http.Request) {
	st, err := s.catalog.Stores(r.Context())
	if err != nil {
		internalError(w, r, err)
		return
	}
	cachePublic(w, 300)
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) deals(w http.ResponseWriter, r *http.Request) {
	d, err := s.catalog.Deals(r.Context(), r.URL.Query().Get("category"), queryInt(r, "limit", 12, 1, 50))
	if err != nil {
		internalError(w, r, err)
		return
	}
	cachePublic(w, 120)
	writeJSON(w, http.StatusOK, d)
}

// redirectToStore registra o clique e manda o usuário para a loja.
func (s *Server) redirectToStore(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "offerID"), 10, 64)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "oferta não encontrada")
		return
	}
	url, err := s.catalog.OfferURL(r.Context(), id)
	if errors.Is(err, catalog.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, "oferta não encontrada")
		return
	}
	if err != nil {
		internalError(w, r, err)
		return
	}
	var uid *int64
	if v, ok := userID(r.Context()); ok {
		uid = &v
	}
	// Não guardamos o IP em claro (LGPD): só um hash para deduplicar cliques.
	if err := s.catalog.RecordClick(r.Context(), id, uid, hashIP(r.RemoteAddr), r.UserAgent(), r.Referer()); err != nil {
		internalError(w, r, err) // não bloquear o usuário seria aceitável; aqui preferimos saber
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, url, http.StatusFound)
}

func hashIP(remote string) string {
	ip, _, err := net.SplitHostPort(remote)
	if err != nil {
		ip = remote
	}
	sum := sha256.Sum256([]byte("comparai:" + ip))
	return hex.EncodeToString(sum[:16])
}

// ---------- auth ----------

type credentials struct {
	Email    string `json:"email"`
	Name     string `json:"name,omitempty"`
	Password string `json:"password"`
}

type tokenResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	User      auth.User `json:"user"`
}

func (s *Server) issue(w http.ResponseWriter, r *http.Request, status int, u auth.User) {
	tok, exp, err := s.auth.IssueToken(u.ID)
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, status, tokenResponse{Token: tok, ExpiresAt: exp, User: u})
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	var c credentials
	if err := decodeJSON(w, r, &c, 4<<10); err != nil {
		writeError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	u, err := s.auth.Register(r.Context(), c.Email, c.Name, c.Password)
	var ve auth.ValidationError
	switch {
	case errors.As(err, &ve):
		writeError(w, r, http.StatusUnprocessableEntity, ve.Msg)
	case errors.Is(err, auth.ErrEmailTaken):
		writeError(w, r, http.StatusConflict, err.Error())
	case err != nil:
		internalError(w, r, err)
	default:
		s.issue(w, r, http.StatusCreated, u)
	}
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var c credentials
	if err := decodeJSON(w, r, &c, 4<<10); err != nil {
		writeError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	u, err := s.auth.Login(r.Context(), c.Email, c.Password)
	switch {
	case errors.Is(err, auth.ErrInvalidCredentials):
		writeError(w, r, http.StatusUnauthorized, err.Error())
	case err != nil:
		internalError(w, r, err)
	default:
		s.issue(w, r, http.StatusOK, u)
	}
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	id, _ := userID(r.Context())
	u, err := s.auth.UserByID(r.Context(), id)
	if errors.Is(err, auth.ErrInvalidToken) {
		writeError(w, r, http.StatusUnauthorized, "usuário não encontrado")
		return
	}
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, u)
}

// ---------- favoritos / alertas / notificações ----------

func (s *Server) productIDFromSlug(w http.ResponseWriter, r *http.Request, slug string) (int64, bool) {
	pid, err := s.catalog.ProductIDBySlug(r.Context(), slug)
	if errors.Is(err, catalog.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, "produto não encontrado")
		return 0, false
	}
	if err != nil {
		internalError(w, r, err)
		return 0, false
	}
	return pid, true
}

func (s *Server) listFavorites(w http.ResponseWriter, r *http.Request) {
	uid, _ := userID(r.Context())
	f, err := s.alerts.Favorites(r.Context(), uid)
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, f)
}

func (s *Server) addFavorite(w http.ResponseWriter, r *http.Request) {
	uid, _ := userID(r.Context())
	pid, ok := s.productIDFromSlug(w, r, chi.URLParam(r, "slug"))
	if !ok {
		return
	}
	if err := s.alerts.AddFavorite(r.Context(), uid, pid); err != nil {
		internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) removeFavorite(w http.ResponseWriter, r *http.Request) {
	uid, _ := userID(r.Context())
	pid, ok := s.productIDFromSlug(w, r, chi.URLParam(r, "slug"))
	if !ok {
		return
	}
	if err := s.alerts.RemoveFavorite(r.Context(), uid, pid); err != nil {
		internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listAlerts(w http.ResponseWriter, r *http.Request) {
	uid, _ := userID(r.Context())
	a, err := s.alerts.Alerts(r.Context(), uid)
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (s *Server) createAlert(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ProductSlug      string `json:"product_slug"`
		TargetPriceCents int64  `json:"target_price_cents"`
	}
	if err := decodeJSON(w, r, &body, 2<<10); err != nil {
		writeError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	if body.TargetPriceCents <= 0 {
		writeError(w, r, http.StatusUnprocessableEntity, "target_price_cents deve ser > 0")
		return
	}
	uid, _ := userID(r.Context())
	pid, ok := s.productIDFromSlug(w, r, body.ProductSlug)
	if !ok {
		return
	}
	id, err := s.alerts.UpsertAlert(r.Context(), uid, pid, body.TargetPriceCents)
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]int64{"id": id})
}

func (s *Server) deleteAlert(w http.ResponseWriter, r *http.Request) {
	uid, _ := userID(r.Context())
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "alerta não encontrado")
		return
	}
	err = s.alerts.DeleteAlert(r.Context(), uid, id)
	if errors.Is(err, alerts.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, "alerta não encontrado")
		return
	}
	if err != nil {
		internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listNotifications(w http.ResponseWriter, r *http.Request) {
	uid, _ := userID(r.Context())
	n, err := s.alerts.Notifications(r.Context(), uid, queryInt(r, "limit", 30, 1, 100))
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, n)
}

func (s *Server) readNotifications(w http.ResponseWriter, r *http.Request) {
	uid, _ := userID(r.Context())
	if err := s.alerts.MarkAllRead(r.Context(), uid); err != nil {
		internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------- ingestão / admin ----------

func (s *Server) ingestOffers(w http.ResponseWriter, r *http.Request) {
	storeID, err := s.ingest.StoreByKey(r.Context(), r.Header.Get("X-Store-Key"))
	if errors.Is(err, ingest.ErrInvalidKey) {
		writeError(w, r, http.StatusUnauthorized, "X-Store-Key inválida")
		return
	}
	if err != nil {
		internalError(w, r, err)
		return
	}
	var body struct {
		Offers []ingest.OfferInput `json:"offers"`
	}
	if err := decodeJSON(w, r, &body, 10<<20); err != nil {
		writeError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	res, err := s.ingest.Import(r.Context(), storeID, body.Offers)
	if errors.Is(err, ingest.ErrBatchSize) {
		writeError(w, r, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) createStore(w http.ResponseWriter, r *http.Request) {
	var in ingest.NewStore
	if err := decodeJSON(w, r, &in, 4<<10); err != nil {
		writeError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	out, err := s.ingest.CreateStore(r.Context(), in)
	switch {
	case errors.Is(err, ingest.ErrStoreExists):
		writeError(w, r, http.StatusConflict, err.Error())
	case errors.As(err, new(ingest.ValidationError)):
		writeError(w, r, http.StatusUnprocessableEntity, err.Error())
	case err != nil:
		internalError(w, r, err)
	default:
		writeJSON(w, http.StatusCreated, out)
	}
}

func (s *Server) createCategory(w http.ResponseWriter, r *http.Request) {
	var in ingest.NewCategory
	if err := decodeJSON(w, r, &in, 4<<10); err != nil {
		writeError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	id, err := s.ingest.CreateCategory(r.Context(), in)
	if errors.As(err, new(ingest.ValidationError)) {
		writeError(w, r, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]int64{"id": id})
}

func constantTimeEqual(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	ha, hb := sha256.Sum256([]byte(a)), sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(ha[:], hb[:]) == 1
}

// spaHandler serve arquivos estáticos e cai em index.html para rotas do frontend.
func spaHandler(static fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(static))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		if _, err := fs.Stat(static, path); err != nil {
			r = r.Clone(r.Context())
			r.URL.Path = "/"
		}
		fileServer.ServeHTTP(w, r)
	})
}
