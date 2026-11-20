package auth

import (
	"testing"
	"time"
)

func TestTokenRoundTrip(t *testing.T) {
	s := NewService(nil, "0123456789abcdef0123456789abcdef", time.Hour)
	tok, _, err := s.IssueToken(42)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.ParseToken(tok)
	if err != nil || id != 42 {
		t.Fatalf("got %d, %v", id, err)
	}

	other := NewService(nil, "ffffffffffffffffffffffffffffffff", time.Hour)
	if _, err := other.ParseToken(tok); err == nil {
		t.Fatal("token assinado com outro segredo foi aceito")
	}

	expired := NewService(nil, "0123456789abcdef0123456789abcdef", -time.Minute)
	tok, _, _ = expired.IssueToken(1)
	if _, err := s.ParseToken(tok); err == nil {
		t.Fatal("token expirado foi aceito")
	}
}
