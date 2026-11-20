package config

import "testing"

func TestCheckSecret(t *testing.T) {
	cases := []struct {
		name, v string
		min     int
		ok      bool
	}{
		{"vazio", "", 16, false},
		{"curto", "abc", 16, false},
		{"exemplo do .env.example", "troque-este-token-admin", 16, false},
		{"admin-dev legado", "admin-dev", 4, false},
		{"valido", "x9f3-token-longo-aleatorio", 16, true},
	}
	for _, c := range cases {
		err := checkSecret("X", c.v, c.min)
		if (err == nil) != c.ok {
			t.Errorf("%s: err=%v, esperado ok=%v", c.name, err, c.ok)
		}
	}
}
