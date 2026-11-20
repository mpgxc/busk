-- Dicionário de palavras do catálogo para correção ortográfica da busca ("você quis dizer").
CREATE MATERIALIZED VIEW search_words AS
SELECT word, ndoc
FROM ts_stat($$
    SELECT to_tsvector('simple', f_unaccent(lower(title || ' ' || coalesce(brand, '')))) FROM products
$$)
WHERE length(word) >= 3;

CREATE UNIQUE INDEX search_words_word_idx ON search_words (word);
CREATE INDEX search_words_trgm_idx ON search_words USING GIN (word gin_trgm_ops);
