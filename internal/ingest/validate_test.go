package ingest

import "testing"

func TestValidEAN(t *testing.T) {
	valid := []string{"7891000315507", "4006381333931", "96385074", "036000291452"}
	for _, c := range valid {
		if !validEAN(c) {
			t.Errorf("%s deveria ser válido", c)
		}
	}
	invalid := []string{"7891000315508", "123", "abcdefghijklm", "789100031550X"}
	for _, c := range invalid {
		if validEAN(c) {
			t.Errorf("%s deveria ser inválido", c)
		}
	}
}

func TestValidateRejectsUnsafeURL(t *testing.T) {
	in := OfferInput{SKU: "1", Title: "x", PriceCents: 100, URL: "javascript:alert(1)"}
	if err := validate(&in); err == nil {
		t.Fatal("javascript: URL aceita")
	}
	in.URL = "https://loja.example/produto/1"
	if err := validate(&in); err != nil {
		t.Fatalf("URL válida rejeitada: %v", err)
	}
}
