# Crawler de preços

O crawler coleta páginas de produto das lojas e alimenta a ingestão que já existe (`POST /api/v1/ingest/offers` por dentro, via `ingest.Service`). Dali em diante, o comparador (EAN → produto → menor preço) funciona igual, seja a oferta vinda de feed ou de crawl.

**Ordem de preferência para obter dados:** feed ou API da loja > programa de afiliados > crawler. O crawler é a última opção, e só para lojas cujos termos permitem.

## Pipeline

```
 sitemap.xml ──► fila (crawl_urls) ──► fetch ──► HTML
   (seed)          ▲   │                          │
                   │   │ links do mesmo host      ├─ JSON-LD schema.org/Product ──► ToOffers ──► batch ──► ingest.Import
                   │   └──────────────────────────┘
                   └─ refresh: cada URL volta para a fila em refresh_minutes
```

1. **Seed.** Um `sitemap.xml` (ou página de listagem) entra na fila com profundidade 0.
2. **Fila.** Um worker reserva URLs vencidas com `FOR UPDATE SKIP LOCKED` e um *lease* de 5 minutos. Se o worker cair, a URL volta sozinha.
3. **Fetch.** Antes de cada requisição: consulta `robots.txt` (cache de 1h), espera a vez no rate limit do host e aplica o `Crawl-delay` se o site pedir.
4. **Extração.**
   - Sitemap XML: vira novas URLs na fila.
   - HTML: lê os blocos `application/ld+json` com `@type: Product` (inclusive dentro de `@graph`) e os links `<a href>`.
5. **Normalização (`ToOffers`).** Converte para `ingest.OfferInput`. Preço vira centavos; EAN só passa se o dígito verificador estiver certo; URL precisa ser http(s); título e SKU são limitados ao tamanho aceito pela ingestão.
6. **Gravação.** Ofertas são acumuladas por loja e gravadas em lotes de 100 pela ingestão, que já cuida do histórico de preço e do menor preço.
7. **Agendamento.** Depois de cada página, a URL volta para a fila em `refresh_minutes`. Se a loja tirar o produto do ar (404/410), a oferta deixa de ser vista e sai pelo `STALE_OFFER_AFTER`.

## Esquema

Migration `internal/db/migrations/0003_crawler.sql`.

| Tabela | Papel | Campos principais |
|---|---|---|
| `crawl_sources` | um host de uma loja | `store_id`, `host`, `rps` (≤ 5), `refresh_minutes` (≥ 15), `max_depth`, `max_urls`, `include_regex`, `enabled` |
| `crawl_urls` | fila e estado de cada URL | `source_id`, `url`, `depth`, `status` (`queued` \| `ok` \| `error` \| `gone` \| `skipped`), `attempts`, `next_at`, `locked_until`, `last_error`, `last_crawled_at` |

O que o crawler **não** guarda: HTML das páginas. Só o que vira oferta, e o restante é descartado.

## Política de coleta

O código aplica estas regras por padrão. Elas não são opcionais.

- **robots.txt** é lido antes de qualquer URL do host. `Disallow` vale para o grupo `ComparaiBot` ou `*`. Um robots.txt com 5xx bloqueia o host inteiro: as URLs ficam `skipped` e voltam no próximo ciclo. Erro de rede ao ler o robots.txt gera nova tentativa com backoff, sem adivinhar. Um 4xx libera tudo, pela convenção do Google.
- **Crawl-delay** é respeitado quando o site informa, e ganha do nosso RPS se for mais lento.
- **Rate limit por host.** O padrão é 0,5 req/s (uma página a cada 2s). O teto no schema é 5 req/s.
- **Identificação.** O User-Agent é `ComparaiBot/1.0 (+contato)`. O contato vem de `CRAWLER_CONTACT`. Configure com um endereço real antes de rodar em produção, para o dono do site poder pedir para parar.
- **Backoff.** 429 e 5xx geram nova tentativa com backoff exponencial (1, 2, 4… minutos, teto de 24h). Um `Retry-After` maior é respeitado.
- **Não seguimos redirect para outro host.** Assim não saímos da loja.
- **Limites de escopo.** `max_depth` (padrão 3), `max_urls` (padrão 20.000) e `include_regex` impedem a fila de crescer sem controle.
- **Sem login, sem captcha, sem contornar bloqueio.** Se a loja bloqueia, a fonte para e a decisão volta para uma pessoa.

Antes de habilitar uma fonte (`enabled = true`), a equipe deve revisar os termos de uso da loja e, de preferência, pedir autorização por escrito. Scraping em produto comercial tem risco jurídico, e isso não é algo que o código resolve sozinho.

**Remoção a pedido da loja:** `DELETE FROM crawl_sources WHERE store_id = …` e `UPDATE stores SET active = false` tiram a loja do ar.

## Operação

Pré-requisito: a loja precisa existir (`POST /api/v1/admin/stores`, que devolve a `api_key` de ingestão; o crawler não usa essa chave, só o `store_id`).

```bash
go run ./cmd/crawler add-source \
  -store loja-exemplo \
  -seed https://loja.example/sitemap.xml \
  -rps 0.5 -refresh-min 360 -max-depth 3 -max-urls 20000 \
  -include '^/(produto|p)/'
```

```bash
go run ./cmd/crawler run -once   # processa o que está vencido e sai
```

```bash
go run ./cmd/crawler run         # fica rodando; dorme 5s quando a fila está vazia
```

Variáveis: `DATABASE_URL`, `CRAWLER_CONTACT`.

Logs saem em JSON: páginas processadas, ofertas recebidas, novas, atualizadas, mudanças de preço e rejeitadas por lote.

## Testes

```bash
go test ./internal/crawler/                                   # unitários: sitemap, JSON-LD, preço, EAN, links
TEST_DATABASE_URL=postgres://…/comparai_test go test -race ./internal/crawler/   # ponta a ponta contra site local
```

O teste ponta a ponta sobe um site HTTP local com robots.txt, sitemap, listagem, produto válido, produto com EAN inválido e 404, e confere: robots.txt respeitado, preço em formato brasileiro, EAN inválido descartado sem derrubar a oferta, 404 marcado como `gone`, e nenhuma nova requisição na segunda rodada.

## Limitações conhecidas

- **Só HTML estático com JSON-LD.** Sites renderizados por JavaScript, sem schema.org de produto, não aparecem. Verifique a cobertura de cada loja antes de habilitar.
- **Casamento só por EAN.** Oferta sem EAN vira produto separado. O próximo passo é agrupar por similaridade de título e marca, com revisão manual.
- **Sem ETag/304.** Toda página é baixada de novo a cada ciclo. Isso é simples e correto (o `last_seen_at` continua atualizado), mas custa banda. Otimizar exige também atualizar `last_seen_at` das ofertas em 304.
- **Rate limit em memória.** Se rodar mais de um processo `run` para o mesmo host, cada um aplica o próprio RPS e o total multiplica. Use um processo por host ou mova o limite para Redis.
- **Um worker sequencial.** Concorrência por host ainda não existe.
- **Sitemap sem `lastmod`.** Toda URL do sitemap é recoletada no ciclo, mesmo sem mudança.

## Próximos passos

1. Feed XML/CSV (Google Shopping) como fonte de primeira classe, reaproveitando `ToOffers` na mesma tabela `crawl_sources`.
2. Métricas por host: taxa de erro, tempo de resposta, páginas com JSON-LD válido. Isso mostra quando uma loja mudou o HTML.
3. ETag/304 com atualização de `last_seen_at`.
4. Matching sem EAN por similaridade de título e marca, com fila de revisão manual.
5. Adaptadores por loja (seletores CSS) para lojas sem JSON-LD, mantidos como exceção.
