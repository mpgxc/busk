package slug

import "testing"

func TestMake(t *testing.T) {
	cases := map[string]string{
		`Notebook Ação 15,6"`:           "notebook-acao-15-6",
		"  Smartphone   Órion X 128GB ": "smartphone-orion-x-128gb",
		"Café & Pão":                    "cafe-pao",
		"---":                           "",
	}
	for in, want := range cases {
		if got := Make(in); got != want {
			t.Errorf("Make(%q) = %q, want %q", in, got, want)
		}
	}
}
