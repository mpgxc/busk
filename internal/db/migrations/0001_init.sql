-- Schema inicial do comparador de preços.
-- Valores monetários são sempre BIGINT em centavos (nunca float).

CREATE EXTENSION IF NOT EXISTS unaccent;
CREATE EXTENSION IF NOT EXISTS pg_trgm;

-- unaccent() não é IMMUTABLE; este wrapper permite usá-lo em índices e colunas geradas.
CREATE OR REPLACE FUNCTION f_unaccent(text) RETURNS text
LANGUAGE sql IMMUTABLE PARALLEL SAFE STRICT AS
$$ SELECT public.unaccent('public.unaccent'::regdictionary, $1) $$;

CREATE TABLE categories (
    id         BIGSERIAL PRIMARY KEY,
    parent_id  BIGINT REFERENCES categories(id) ON DELETE SET NULL,
    slug       TEXT NOT NULL UNIQUE,
    name       TEXT NOT NULL
);

CREATE TABLE stores (
    id            BIGSERIAL PRIMARY KEY,
    slug          TEXT NOT NULL UNIQUE,
    name          TEXT NOT NULL,
    website       TEXT NOT NULL,
    logo_url      TEXT,
    rating        NUMERIC(2,1),
    api_key_hash  TEXT NOT NULL UNIQUE, -- sha256 hex da chave de ingestão
    active        BOOLEAN NOT NULL DEFAULT true,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE products (
    id               BIGSERIAL PRIMARY KEY,
    slug             TEXT NOT NULL UNIQUE,
    title            TEXT NOT NULL,
    brand            TEXT,
    ean              TEXT UNIQUE,
    category_id      BIGINT REFERENCES categories(id) ON DELETE SET NULL,
    image_url        TEXT,
    description      TEXT,
    specs            JSONB NOT NULL DEFAULT '{}'::jsonb,
    -- desnormalizado a partir de offers (atualizado na ingestão)
    min_price_cents  BIGINT,
    max_price_cents  BIGINT,
    offers_count     INT NOT NULL DEFAULT 0,
    search           tsvector GENERATED ALWAYS AS (
        setweight(to_tsvector('portuguese', f_unaccent(coalesce(title, ''))), 'A') ||
        setweight(to_tsvector('portuguese', f_unaccent(coalesce(brand, ''))), 'B')
    ) STORED,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX products_search_idx     ON products USING GIN (search);
CREATE INDEX products_title_trgm_idx ON products USING GIN (f_unaccent(lower(title)) gin_trgm_ops);
CREATE INDEX products_category_idx   ON products (category_id);
CREATE INDEX products_min_price_idx  ON products (min_price_cents) WHERE min_price_cents IS NOT NULL;
CREATE INDEX products_brand_idx      ON products (lower(brand));

CREATE TABLE offers (
    id                 BIGSERIAL PRIMARY KEY,
    product_id         BIGINT NOT NULL REFERENCES products(id) ON DELETE CASCADE,
    store_id           BIGINT NOT NULL REFERENCES stores(id) ON DELETE CASCADE,
    sku                TEXT NOT NULL,
    title              TEXT NOT NULL,
    url                TEXT NOT NULL,
    price_cents        BIGINT NOT NULL CHECK (price_cents > 0),
    list_price_cents   BIGINT CHECK (list_price_cents > 0),   -- preço "de"
    pix_price_cents    BIGINT CHECK (pix_price_cents > 0),    -- preço à vista no PIX
    installments       INT CHECK (installments BETWEEN 1 AND 48),
    installment_cents  BIGINT CHECK (installment_cents > 0),
    shipping_cents     BIGINT CHECK (shipping_cents >= 0),    -- NULL = desconhecido, 0 = grátis
    in_stock           BOOLEAN NOT NULL DEFAULT true,
    last_seen_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (store_id, sku)
);
CREATE INDEX offers_product_idx   ON offers (product_id);
CREATE INDEX offers_last_seen_idx ON offers (last_seen_at) WHERE in_stock;

-- Cada mudança de preço de uma oferta.
CREATE TABLE price_history (
    id           BIGSERIAL PRIMARY KEY,
    offer_id     BIGINT NOT NULL REFERENCES offers(id) ON DELETE CASCADE,
    price_cents  BIGINT NOT NULL,
    recorded_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX price_history_offer_idx ON price_history (offer_id, recorded_at);

-- Menor preço do produto por dia (fuso America/Sao_Paulo); alimenta gráficos e "ofertas do dia".
CREATE TABLE product_price_daily (
    product_id       BIGINT NOT NULL REFERENCES products(id) ON DELETE CASCADE,
    day              DATE NOT NULL,
    min_price_cents  BIGINT NOT NULL,
    PRIMARY KEY (product_id, day)
);

CREATE TABLE users (
    id             BIGSERIAL PRIMARY KEY,
    email          TEXT NOT NULL UNIQUE,
    name           TEXT NOT NULL,
    password_hash  TEXT NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE favorites (
    user_id     BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    product_id  BIGINT NOT NULL REFERENCES products(id) ON DELETE CASCADE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, product_id)
);

CREATE TABLE price_alerts (
    id                  BIGSERIAL PRIMARY KEY,
    user_id             BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    product_id          BIGINT NOT NULL REFERENCES products(id) ON DELETE CASCADE,
    target_price_cents  BIGINT NOT NULL CHECK (target_price_cents > 0),
    active              BOOLEAN NOT NULL DEFAULT true,
    triggered_at        TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (user_id, product_id)
);
CREATE INDEX price_alerts_active_idx ON price_alerts (product_id) WHERE active;

CREATE TABLE notifications (
    id           BIGSERIAL PRIMARY KEY,
    user_id      BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    product_id   BIGINT REFERENCES products(id) ON DELETE CASCADE,
    kind         TEXT NOT NULL,
    message      TEXT NOT NULL,
    read_at      TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX notifications_user_idx ON notifications (user_id, created_at DESC);

-- Cliques de saída para a loja (base para comissionamento/afiliados).
CREATE TABLE clicks (
    id          BIGSERIAL PRIMARY KEY,
    offer_id    BIGINT NOT NULL REFERENCES offers(id) ON DELETE CASCADE,
    user_id     BIGINT REFERENCES users(id) ON DELETE SET NULL,
    ip_hash     TEXT,
    user_agent  TEXT,
    referer     TEXT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX clicks_offer_idx ON clicks (offer_id, created_at);
