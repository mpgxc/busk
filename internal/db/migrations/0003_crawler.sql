-- Crawler: uma fonte por host de loja e uma fila de URLs com o estado de cada uma.

CREATE TABLE crawl_sources (
    id              BIGSERIAL PRIMARY KEY,
    store_id        BIGINT NOT NULL REFERENCES stores(id) ON DELETE CASCADE,
    host            TEXT NOT NULL,                              -- ex.: www.loja.example[:porta]
    rps             NUMERIC(6,3) NOT NULL DEFAULT 0.5 CHECK (rps > 0 AND rps <= 5),
    refresh_minutes INT NOT NULL DEFAULT 360 CHECK (refresh_minutes >= 15),
    max_depth       INT NOT NULL DEFAULT 3 CHECK (max_depth BETWEEN 0 AND 10),
    max_urls        INT NOT NULL DEFAULT 20000 CHECK (max_urls > 0),
    include_regex   TEXT,                                       -- só segue links cujo caminho casa com isto
    enabled         BOOLEAN NOT NULL DEFAULT true,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (store_id, host)
);

CREATE TABLE crawl_urls (
    id              BIGSERIAL PRIMARY KEY,
    source_id       BIGINT NOT NULL REFERENCES crawl_sources(id) ON DELETE CASCADE,
    url             TEXT NOT NULL,
    depth           INT NOT NULL DEFAULT 0,
    status          TEXT NOT NULL DEFAULT 'queued'
                    CHECK (status IN ('queued', 'ok', 'error', 'gone', 'skipped')),
    attempts        INT NOT NULL DEFAULT 0,
    next_at         TIMESTAMPTZ NOT NULL DEFAULT now(),         -- quando é elegível de novo
    locked_until    TIMESTAMPTZ,                                -- lease do worker; expira se o worker cair
    last_error      TEXT,
    last_crawled_at TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (source_id, url)
);

CREATE INDEX crawl_urls_due_idx ON crawl_urls (next_at);
