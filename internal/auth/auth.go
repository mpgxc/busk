// Package auth cuida de cadastro, login (bcrypt) e tokens JWT.
package auth

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

var (
	ErrEmailTaken         = errors.New("email já cadastrado")
	ErrInvalidCredentials = errors.New("email ou senha inválidos")
	ErrInvalidToken       = errors.New("token inválido")
)

type User struct {
	ID        int64     `json:"id"`
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

type Service struct {
	db     *pgxpool.Pool
	secret []byte
	ttl    time.Duration
}

func NewService(db *pgxpool.Pool, secret string, ttl time.Duration) *Service {
	return &Service{db: db, secret: []byte(secret), ttl: ttl}
}

// ValidationError é devolvido como 422 pela camada HTTP.
type ValidationError struct{ Msg string }

func (e ValidationError) Error() string { return e.Msg }

func normalizeEmail(e string) string { return strings.ToLower(strings.TrimSpace(e)) }

func (s *Service) Register(ctx context.Context, email, name, password string) (User, error) {
	email = normalizeEmail(email)
	name = strings.TrimSpace(name)
	if _, err := mail.ParseAddress(email); err != nil || len(email) > 254 {
		return User{}, ValidationError{"email inválido"}
	}
	if name == "" || len(name) > 120 {
		return User{}, ValidationError{"nome obrigatório (máx. 120)"}
	}
	if len(password) < 8 || len(password) > 72 { // 72 = limite do bcrypt
		return User{}, ValidationError{"senha deve ter entre 8 e 72 caracteres"}
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return User{}, err
	}
	var u User
	err = s.db.QueryRow(ctx, `INSERT INTO users (email, name, password_hash) VALUES ($1,$2,$3)
		RETURNING id, email, name, created_at`, email, name, string(hash)).Scan(&u.ID, &u.Email, &u.Name, &u.CreatedAt)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return User{}, ErrEmailTaken
	}
	return u, err
}

// dummyHash evita timing attack que revela se o email existe.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("dummy-password"), bcrypt.DefaultCost)

func (s *Service) Login(ctx context.Context, email, password string) (User, error) {
	var u User
	var hash string
	err := s.db.QueryRow(ctx, `SELECT id, email, name, created_at, password_hash FROM users WHERE email = $1`,
		normalizeEmail(email)).Scan(&u.ID, &u.Email, &u.Name, &u.CreatedAt, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
		return User{}, ErrInvalidCredentials
	}
	if err != nil {
		return User{}, err
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return User{}, ErrInvalidCredentials
	}
	return u, nil
}

func (s *Service) UserByID(ctx context.Context, id int64) (User, error) {
	var u User
	err := s.db.QueryRow(ctx, `SELECT id, email, name, created_at FROM users WHERE id = $1`, id).
		Scan(&u.ID, &u.Email, &u.Name, &u.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return u, ErrInvalidToken
	}
	return u, err
}

func (s *Service) IssueToken(userID int64) (string, time.Time, error) {
	exp := time.Now().Add(s.ttl)
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
		Subject:   fmt.Sprint(userID),
		IssuedAt:  jwt.NewNumericDate(time.Now()),
		ExpiresAt: jwt.NewNumericDate(exp),
		Issuer:    "comparai",
	})
	signed, err := tok.SignedString(s.secret)
	return signed, exp, err
}

func (s *Service) ParseToken(raw string) (int64, error) {
	var claims jwt.RegisteredClaims
	_, err := jwt.ParseWithClaims(raw, &claims, func(t *jwt.Token) (any, error) {
		return s.secret, nil
	}, jwt.WithValidMethods([]string{"HS256"}), jwt.WithIssuer("comparai"), jwt.WithExpirationRequired())
	if err != nil {
		return 0, ErrInvalidToken
	}
	var id int64
	if _, err := fmt.Sscan(claims.Subject, &id); err != nil || id <= 0 {
		return 0, ErrInvalidToken
	}
	return id, nil
}
