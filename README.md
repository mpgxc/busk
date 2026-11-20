# Comparaí

Comparador de preços: as lojas parceiras enviam o feed de ofertas, e o site mostra o menor preço de cada produto entre as lojas, o histórico de preço, as ofertas do dia e alertas de "baixou o preço".

- **Backend:** Go 1.26, [chi](https://github.com/go-chi/chi), [pgx v5](https://github.com/jackc/pgx), PostgreSQL 16
- **Frontend:** SPA em JavaScript puro, embutido no binário (`web/static`), sem build step
- **Um binário só:** API, frontend, migrations e worker de alertas

## Rodando

### Com Docker

```bash
docker compose up -d --build
```

```bash
docker compose exec api /seed
```

Abra http://localhost:8080. Usuário demo: `demo@comparai.dev` / `demo12345`.

### Sem Docker (Postgres local)

```bash
cp .env.example .env
```

```bash
make seed
```

```bash
make run
```

O PostgreSQL precisa das extensões `unaccent` e `pg_trgm` (vêm no `postgresql-contrib` e na imagem oficial). As migrations rodam sozinhas na subida, protegidas por advisory lock.

### Testes

```bash
make test
```

```bash
make test-integration TEST_DATABASE_URL=postgres://postgres:postgres@localhost:5432/comparai_test?sslmode=disable
```

O teste de integração (`internal/httpapi/api_test.go`) sobe a API com `httptest` e percorre o fluxo inteiro. Ele **apaga o schema `public`** do banco de teste.

## Funcionalidades

| Feature | Como funciona |
|---|---|
| Busca | Full-text em português (stemming, sem acento) + similaridade trigram para erro de digitação |
| "Você quis dizer" | Se a busca não acha nada, cada termo é corrigido pela palavra mais parecida do catálogo (`search_words`) |
| Filtros e facets | Categoria (inclui subcategorias), marca, loja, faixa de preço; contagens por facet; 5 ordenações |
| Autocomplete | `/api/v1/suggest` com debounce no frontend |
| Página do produto | Ofertas ordenadas por *melhor preço + frete*, preço no PIX, parcelamento, preço "de" |
| Histórico | Menor preço diário (fuso de Brasília) com gráfico de 30/90/180 dias e veredito: abaixo, na média ou acima da média de 30 dias |
| Ofertas do dia | Produtos com maior queda do preço atual em relação à média dos 30 dias anteriores |
| Conta | Cadastro e login (bcrypt + JWT), favoritos, alertas de preço, notificações |
| Alertas | Worker em background dispara quando o menor preço ≤ alvo; `FOR UPDATE SKIP LOCKED` evita aviso duplicado com várias réplicas |
| Ofertas vencidas | Oferta que a loja parou de enviar há mais de `STALE_OFFER_AFTER` sai do menor preço |
| Clique de saída | `/r/{offerID}` registra o clique (base para afiliados/CPC) e redireciona para a loja; o IP é guardado só como hash (LGPD) |

## Modelo de dados

```
categories ─┐
            └─< products >── offers >── stores
                   │           └─< price_history   (cada mudança de preço)
                   ├─< product_price_daily          (menor preço por dia)
                   ├─< favorites >── users
                   ├─< price_alerts >── users
                   └─< notifications >── users
offers ─< clicks
```

- **Dinheiro sempre em centavos (`BIGINT`)**, nunca float. A API recebe e devolve `*_cents`.
- `products.min_price_cents`, `max_price_cents` e `offers_count` são desnormalizados e recalculados a cada ingestão (`ingest.RefreshProducts`), para a busca não precisar agregar ofertas.
- O "melhor preço" de uma oferta é `LEAST(price, pix_price)`.

## API

Base: `/api/v1`. Erros: `{"error": "...", "request_id": "..."}`.

### Pública

| Método | Rota | Parâmetros |
|---|---|---|
| GET | `/search` | `q`, `category`, `brand` (repetível ou `a,b`), `store`, `min_price`, `max_price` (em reais), `sort` = `relevance` \| `price_asc` \| `price_desc` \| `offers` \| `newest`, `page`, `per_page` (máx. 60) |
| GET | `/suggest` | `q`, `limit` |
| GET | `/products/{slug}` | — |
| GET | `/products/{slug}/history` | `days` (7–365) |
| GET | `/categories` | árvore |
| GET | `/stores` | — |
| GET | `/deals` | `category`, `limit` |
| POST | `/auth/register` | `{email, name, password}` → `{token, expires_at, user}` |
| POST | `/auth/login` | `{email, password}` |
| GET | `/r/{offerID}` | (fora de `/api/v1`) redirect 302 para a loja |

### Usuário (`Authorization: Bearer <token>`)

`GET /me` · `GET /me/favorites` · `PUT|DELETE /me/favorites/{slug}` · `GET /me/alerts` · `POST /me/alerts {product_slug, target_price_cents}` · `DELETE /me/alerts/{id}` · `GET /me/notifications` · `POST /me/notifications/read`

### Integração com lojas (`X-Store-Key`)

`POST /ingest/offers`: lote de até 1.000 ofertas, processado numa transação com savepoint por item. Um item inválido é rejeitado sozinho, sem derrubar o lote.

```bash
curl -X POST localhost:8080/api/v1/ingest/offers \
  -H "X-Store-Key: ck_..." -H "Content-Type: application/json" \
  -d '{"offers":[{"sku":"123","ean":"7891000315507","title":"Air Fryer 5L","brand":"Brisa",
       "category":"eletrodomesticos","url":"https://loja.example/p/123",
       "price_cents":39990,"pix_price_cents":35990,"installments":10,"installment_cents":3999,
       "shipping_cents":0,"in_stock":true}]}'
```

Resposta: `{"received":1,"created":1,"updated":0,"price_changes":1,"new_products":0,"rejected":[]}`

**Casamento de produto:** pelo EAN/GTIN (com dígito verificador validado). Sem EAN, a oferta fica presa ao produto da primeira ingestão daquele `sku`; se nunca foi vista, vira um produto novo. URLs precisam ser `http(s)`, porque viram redirect público.

### Admin (`Authorization: Bearer $ADMIN_TOKEN`)

- `POST /admin/stores {slug, name, website, logo_url?, rating?}` devolve a `api_key` da loja **uma única vez** (no banco fica só o SHA-256).
- `POST /admin/categories {slug, name, parent_slug?}`

## Configuração

Veja `.env.example`. Obrigatórias: `JWT_SECRET` (≥ 32 caracteres) e `ADMIN_TOKEN`. Atrás de load balancer, use `TRUST_PROXY=true` para o rate limit usar o IP real (não ligue sem proxy, porque o `X-Forwarded-For` pode ser forjado).

## Estrutura

```
cmd/api            main: config, migrations, worker, HTTP com graceful shutdown
cmd/seed           dados de demonstração (lojas e marcas fictícias)
internal/catalog   consultas de leitura: busca, facets, produto, histórico, ofertas do dia
internal/ingest    feed das lojas, casamento por EAN, histórico, admin de lojas e categorias
internal/alerts    favoritos, alertas, notificações, worker
internal/auth      cadastro, login, JWT
internal/httpapi   rotas, middlewares (request id, log JSON, rate limit, CORS), handlers
internal/db        pool pgx + migrations embutidas (internal/db/migrations)
web/static         frontend
```

## Próximos passos sugeridos

- **Escala da busca:** com milhões de produtos, mover a busca para OpenSearch/Meilisearch e deixar o Postgres como fonte da verdade.
- **Ofertas do dia:** materializar em tabela/view atualizada pelo worker em vez de calcular por request.
- **Cache:** Redis na frente de `/search` e `/products/{slug}` (as respostas já mandam `Cache-Control`, então dá para começar com CDN).
- **Rate limit distribuído:** hoje é em memória, por instância.
- **Notificações reais:** implementar `alerts.Notifier` com e-mail, push ou WhatsApp.
- **Afiliados:** parâmetros de tracking por loja no redirect e relatório de cliques por loja.
- **Coleta ativa:** além do push via API, um job que puxa feeds XML/CSV (Google Shopping) das lojas.
- **Matching sem EAN:** agrupar ofertas por similaridade de título/marca com revisão manual.
