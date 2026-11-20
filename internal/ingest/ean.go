package ingest

// ValidEAN diz se o código é um GTIN-8/12/13/14 com dígito verificador correto.
// Exportado para o crawler descartar EAN inválido antes da ingestão.
func ValidEAN(code string) bool { return validEAN(code) }
