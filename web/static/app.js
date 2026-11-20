// Frontend do Comparaí: SPA sem dependências que consome a API /api/v1.

const app = document.getElementById("app");
const brl = new Intl.NumberFormat("pt-BR", { style: "currency", currency: "BRL" });
const money = (cents) => (cents == null ? "—" : brl.format(cents / 100));
const esc = (s) =>
  String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);

// ---------- sessão ----------
const store = {
  get(k) { try { return localStorage.getItem(k); } catch { return null; } },
  set(k, v) { try { v == null ? localStorage.removeItem(k) : localStorage.setItem(k, v); } catch { /* modo privado */ } },
};
let session = JSON.parse(store.get("session") || "null");
const setSession = (s) => { session = s; store.set("session", s ? JSON.stringify(s) : null); renderNav(); };

async function api(path, { method = "GET", body, auth = false } = {}) {
  const headers = { "Content-Type": "application/json" };
  if (session?.token) headers.Authorization = `Bearer ${session.token}`;
  const res = await fetch(`/api/v1${path}`, { method, headers, body: body ? JSON.stringify(body) : undefined });
  if (res.status === 401 && auth) {
    setSession(null);
    navigate(`/entrar?next=${encodeURIComponent(location.pathname + location.search)}`);
    throw new Error("Faça login para continuar");
  }
  if (res.status === 204) return null;
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data.error || `Erro ${res.status}`);
  return data;
}

function toast(msg) {
  const el = document.createElement("div");
  el.className = "toast";
  el.setAttribute("role", "status");
  el.textContent = msg;
  document.body.append(el);
  setTimeout(() => el.remove(), 2800);
}

// ---------- roteador ----------
const routes = [
  [/^\/$/, home],
  [/^\/busca$/, search],
  [/^\/produto\/([^/]+)$/, product],
  [/^\/entrar$/, () => authPage("login")],
  [/^\/cadastro$/, () => authPage("register")],
  [/^\/conta$/, account],
];

function navigate(url, { replace = false } = {}) {
  history[replace ? "replaceState" : "pushState"](null, "", url);
  render();
}

document.addEventListener("click", (e) => {
  const a = e.target.closest("a[data-link]");
  if (!a || e.metaKey || e.ctrlKey || e.shiftKey || a.target === "_blank") return;
  e.preventDefault();
  navigate(a.getAttribute("href"));
});
window.addEventListener("popstate", render);

async function render() {
  hideSuggest();
  const path = location.pathname;
  const params = new URLSearchParams(location.search);
  document.getElementById("search-input").value = path === "/busca" ? params.get("q") || "" : "";
  for (const [re, view] of routes) {
    const m = path.match(re);
    if (m) {
      app.innerHTML = `<p class="muted">Carregando…</p>`;
      try {
        await view(params, ...m.slice(1));
      } catch (err) {
        app.innerHTML = `<div class="card empty">${esc(err.message)}</div>`;
      }
      window.scrollTo(0, 0);
      return;
    }
  }
  app.innerHTML = `<div class="card empty">Página não encontrada. <a href="/" data-link>Voltar ao início</a></div>`;
}

function renderNav() {
  const nav = document.getElementById("account-nav");
  nav.innerHTML = session
    ? `<a href="/conta" data-link>Olá, ${esc(session.user.name.split(" ")[0])}</a>
       <button class="ghost" id="logout" type="button">Sair</button>`
    : `<a href="/entrar" data-link>Entrar</a><a href="/cadastro" class="btn" data-link>Criar conta</a>`;
  nav.querySelector("#logout")?.addEventListener("click", () => { setSession(null); navigate("/"); });
}

// ---------- componentes ----------
function thumb(p) {
  if (p.image_url) return `<div class="thumb"><img src="${esc(p.image_url)}" alt="" loading="lazy"></div>`;
  let h = 0;
  for (const c of p.title) h = (h * 31 + c.charCodeAt(0)) % 360;
  const initials = (p.brand || p.title).slice(0, 2).toUpperCase();
  return `<div class="thumb" style="background:hsl(${h} 45% 48%)" aria-hidden="true">${esc(initials)}</div>`;
}

function productCard(p, badge = "") {
  return `<a class="card pcard" href="/produto/${encodeURIComponent(p.slug)}" data-link>
    ${thumb(p)}${badge}
    <span class="title">${esc(p.title)}</span>
    <span class="price-label">menor preço${p.offers_count > 1 ? ` em ${p.offers_count} lojas` : ""}</span>
    <span class="price">${money(p.min_price_cents)}</span>
  </a>`;
}

const grid = (items, fn = productCard) =>
  items.length ? `<div class="grid">${items.map((i) => fn(i)).join("")}</div>` : `<div class="card empty">Nada por aqui ainda.</div>`;

// ---------- páginas ----------
async function home() {
  document.title = "Comparaí — compare preços";
  const [deals, cats, stores] = await Promise.all([api("/deals?limit=12"), api("/categories"), api("/stores")]);
  app.innerHTML = `
    <section class="section">
      <h1>Compare antes de comprar</h1>
      <p class="muted">Preços de ${stores.length} lojas, histórico dos últimos 90 dias e alerta quando baixar.</p>
      <div class="chips">${cats
        .flatMap((c) => [c, ...(c.children || [])])
        .map((c) => `<a class="chip" href="/busca?category=${c.slug}" data-link>${esc(c.name)}</a>`)
        .join("")}</div>
    </section>
    <section class="section">
      <h2>Ofertas do dia</h2>
      <p class="muted small">Produtos com o preço atual mais abaixo da média dos últimos 30 dias.</p>
      ${grid(deals, (d) => productCard(d, `<span class="badge good">−${d.drop_percent.toLocaleString("pt-BR")}%</span>`))}
    </section>
    <section class="section">
      <h2>Lojas parceiras</h2>
      <div class="chips">${stores
        .map((s) => `<a class="chip" href="/busca?store=${s.slug}" data-link>${esc(s.name)}${s.rating ? ` · ★ ${s.rating.toFixed(1)}` : ""}</a>`)
        .join("")}</div>
    </section>`;
}

async function search(params) {
  const q = params.get("q") || "";
  const page = Number(params.get("page") || 1);
  const res = await api(`/search?${params.toString()}`);
  document.title = q ? `${q} — Comparaí` : "Busca — Comparaí";

  const selected = (k) => new Set(params.getAll(k).flatMap((v) => v.split(",")));
  const brands = selected("brand");
  const storesSel = selected("store");
  const checkbox = (name, f, set) => `<label><input type="checkbox" name="${name}" value="${esc(f.value)}" ${set.has(f.value) ? "checked" : ""}>
    ${esc(f.label)}<span class="count">${f.count}</span></label>`;
  const pages = Math.max(1, Math.ceil(res.total / res.per_page));
  const pageLink = (n, label) => {
    const p = new URLSearchParams(params);
    p.set("page", n);
    return `<a class="btn ghost" href="/busca?${p}" data-link>${label}</a>`;
  };
  const category = params.get("category");

  app.innerHTML = `
    ${res.corrected_query ? `<div class="card notice">Mostrando resultados para <strong>${esc(res.corrected_query)}</strong>. Nenhum resultado para “${esc(q)}”.</div>` : ""}
    <div class="results">
      <form class="card filters" id="filters">
        <h3>Categoria</h3>
        ${category ? `<label><input type="radio" name="category" value="" > Todas</label>` : ""}
        ${res.categories.map((f) => `<label><input type="radio" name="category" value="${esc(f.value)}" ${f.value === category ? "checked" : ""}>${esc(f.label)}<span class="count">${f.count}</span></label>`).join("") || `<p class="muted small">—</p>`}
        <h3>Marca</h3>
        ${res.brands.map((f) => checkbox("brand", f, brands)).join("") || `<p class="muted small">—</p>`}
        <h3>Loja</h3>
        ${res.stores.map((f) => checkbox("store", f, storesSel)).join("") || `<p class="muted small">—</p>`}
        <h3>Preço (R$)</h3>
        <div class="range">
          <input type="number" name="min_price" min="0" placeholder="mín." value="${esc(params.get("min_price") || "")}" aria-label="Preço mínimo">
          <input type="number" name="max_price" min="0" placeholder="máx." value="${esc(params.get("max_price") || "")}" aria-label="Preço máximo">
        </div>
        <p><button type="submit">Aplicar</button></p>
      </form>
      <div>
        <div class="toolbar">
          <div>
            <h1>${q ? `“${esc(res.corrected_query || q)}”` : "Todos os produtos"}</h1>
            <span class="muted small">${res.total} produto${res.total === 1 ? "" : "s"}</span>
          </div>
          <div class="actions">
            <button type="button" class="ghost filter-toggle" id="toggle-filters">Filtros</button>
            <select id="sort" aria-label="Ordenar">
              ${[["relevance", "Mais relevantes"], ["price_asc", "Menor preço"], ["price_desc", "Maior preço"], ["offers", "Mais lojas"], ["newest", "Novidades"]]
                .map(([v, l]) => `<option value="${v}" ${params.get("sort") === v ? "selected" : ""}>${l}</option>`).join("")}
            </select>
          </div>
        </div>
        ${grid(res.items)}
        ${pages > 1 ? `<nav class="pager">${page > 1 ? pageLink(page - 1, "← Anterior") : ""}
          <span class="muted" style="align-self:center">Página ${page} de ${pages}</span>
          ${page < pages ? pageLink(page + 1, "Próxima →") : ""}</nav>` : ""}
      </div>
    </div>`;

  const form = document.getElementById("filters");
  const apply = () => {
    const p = new URLSearchParams();
    if (q) p.set("q", q);
    const fd = new FormData(form);
    for (const [k, v] of fd) if (v) p.append(k, v);
    const sort = document.getElementById("sort").value;
    if (sort !== "relevance") p.set("sort", sort);
    navigate(`/busca?${p}`);
  };
  form.addEventListener("submit", (e) => { e.preventDefault(); apply(); });
  form.addEventListener("change", (e) => { if (e.target.type !== "number") apply(); });
  document.getElementById("sort").addEventListener("change", apply);
  document.getElementById("toggle-filters").addEventListener("click", () => form.classList.toggle("open"));
}

function verdictBadge(v) {
  const cls = v === "abaixo da média" ? "good" : v === "acima da média" ? "bad" : "neutral";
  return `<span class="badge ${cls}">${esc(v)}</span>`;
}

async function product(_params, slug) {
  const p = await api(`/products/${encodeURIComponent(slug)}`);
  document.title = `${p.title} — Comparaí`;
  const bestIdx = p.offers.findIndex((o) => o.in_stock);
  const specs = Object.entries(p.specs || {});

  app.innerHTML = `
    <p class="small muted">
      <a href="/" data-link>Início</a>
      ${p.category_slug ? ` › <a href="/busca?category=${p.category_slug}" data-link>${esc(p.category_name)}</a>` : ""}
    </p>
    <div class="product section">
      <div class="card" style="padding:16px">${thumb(p)}</div>
      <div class="card product-head">
        <h1>${esc(p.title)}</h1>
        <div class="muted small">${p.brand ? `Marca: ${esc(p.brand)}` : ""}${p.ean ? ` · EAN ${esc(p.ean)}` : ""}</div>
        <div class="best">
          <div>
            <div class="price-label">menor preço ${p.offers_count > 1 ? `entre ${p.offers_count} lojas` : ""}</div>
            <div class="price">${money(p.min_price_cents)}</div>
          </div>
          <div>
            ${verdictBadge(p.stats.verdict)}
            <div class="small muted">média 30 dias: ${money(p.stats.average_30d_cents)} · menor em 90 dias: ${money(p.stats.lowest_90d_cents)}</div>
          </div>
        </div>
        <div class="actions">
          ${bestIdx >= 0 ? `<a class="btn" href="${p.offers[bestIdx].redirect_url}" target="_blank" rel="noopener nofollow">Ir à loja mais barata</a>` : ""}
          <button class="ghost" type="button" id="fav">♡ Favoritar</button>
          <form class="alert-form" id="alert-form">
            <label class="small muted" for="target">Avise-me abaixo de R$</label>
            <input id="target" type="number" step="0.01" min="1" required
              value="${p.min_price_cents ? Math.floor((p.min_price_cents * 0.9) / 100) : ""}">
            <button class="ghost" type="submit">Criar alerta</button>
          </form>
        </div>
      </div>
    </div>

    <section class="section">
      <h2>Preços em ${p.offers.length} loja${p.offers.length === 1 ? "" : "s"}</h2>
      <div class="card offers-wrap">
        ${p.offers.length ? `<table class="offers">
          <thead><tr><th>Loja</th><th class="num">Preço</th><th class="num">No PIX</th><th class="num hide-sm">Parcelado</th><th class="num">Frete</th><th class="num">Total</th><th></th></tr></thead>
          <tbody>${p.offers.map((o, i) => `
            <tr class="${i === bestIdx ? "offer-best" : ""}">
              <td class="store"><strong>${esc(o.store_name)}</strong>${o.store_rating ? `<div class="small muted">★ ${o.store_rating.toFixed(1)}</div>` : ""}
                ${o.in_stock ? "" : `<div><span class="badge neutral">indisponível</span></div>`}</td>
              <td class="num" data-label="Preço">${o.list_price_cents && o.list_price_cents > o.price_cents ? `<div class="small muted"><s>${money(o.list_price_cents)}</s></div>` : ""}${money(o.price_cents)}</td>
              <td class="num" data-label="No PIX">${o.pix_price_cents ? money(o.pix_price_cents) : "—"}</td>
              <td class="num hide-sm" data-label="Parcelado">${o.installments ? `${o.installments}x ${money(o.installment_cents)}` : "—"}</td>
              <td class="num" data-label="Frete">${o.shipping_cents == null ? `<span class="muted small">a calcular</span>` : o.shipping_cents === 0 ? `<span class="badge good">grátis</span>` : money(o.shipping_cents)}</td>
              <td class="num" data-label="Total"><strong>${money(o.total_cents ?? o.best_price_cents)}</strong></td>
              <td class="num go"><a class="btn ${i === bestIdx ? "" : "ghost"}" href="${o.redirect_url}" target="_blank" rel="noopener nofollow">Ver</a></td>
            </tr>`).join("")}</tbody>
        </table>` : `<div class="empty">Nenhuma oferta disponível no momento.</div>`}
      </div>
    </section>

    <section class="section">
      <div class="toolbar">
        <h2 style="margin:0">Histórico do menor preço</h2>
        <div class="seg" role="group" aria-label="Período">
          ${[30, 90, 180].map((d) => `<button type="button" data-days="${d}" aria-pressed="${d === 90}">${d} dias</button>`).join("")}
        </div>
      </div>
      <div class="card chart" id="chart"><p class="muted">Carregando…</p></div>
    </section>

    ${specs.length ? `<section class="section"><h2>Especificações</h2><div class="card"><table class="specs"><tbody>
      ${specs.map(([k, v]) => `<tr><td>${esc(k)}</td><td>${esc(v === true ? "sim" : v === false ? "não" : v)}</td></tr>`).join("")}
    </tbody></table></div></section>` : ""}`;

  const loadChart = async (days) => {
    document.querySelectorAll(".seg button").forEach((b) => b.setAttribute("aria-pressed", String(Number(b.dataset.days) === days)));
    const pts = await api(`/products/${encodeURIComponent(slug)}/history?days=${days}`);
    const el = document.getElementById("chart");
    el.innerHTML = priceChart(pts, el.clientWidth - 32);
  };
  document.querySelectorAll(".seg button").forEach((b) => b.addEventListener("click", () => loadChart(Number(b.dataset.days))));
  loadChart(90);

  document.getElementById("fav").addEventListener("click", async () => {
    if (!session) return navigate(`/entrar?next=${encodeURIComponent(location.pathname)}`);
    await api(`/me/favorites/${encodeURIComponent(slug)}`, { method: "PUT", auth: true });
    toast("Adicionado aos favoritos");
  });
  document.getElementById("alert-form").addEventListener("submit", async (e) => {
    e.preventDefault();
    if (!session) return navigate(`/entrar?next=${encodeURIComponent(location.pathname)}`);
    const cents = Math.round(parseFloat(document.getElementById("target").value.replace(",", ".")) * 100);
    try {
      await api("/me/alerts", { method: "POST", auth: true, body: { product_slug: slug, target_price_cents: cents } });
      toast(`Alerta criado: avisaremos abaixo de ${money(cents)}`);
    } catch (err) { toast(err.message); }
  });
}

// Gráfico de linha em SVG puro.
// A largura do viewBox acompanha o container para o texto não encolher no celular.
function priceChart(pts, width) {
  if (pts.length < 2) return `<p class="muted">Ainda não há histórico suficiente.</p>`;
  const W = Math.max(280, Math.round(width) || 760), H = W < 500 ? 200 : 240, L = 72, R = 12, T = 12, B = 28;
  const vals = pts.map((p) => p.min_price_cents);
  let lo = Math.min(...vals), hi = Math.max(...vals);
  const pad = Math.max((hi - lo) * 0.15, hi * 0.02);
  lo -= pad; hi += pad;
  const x = (i) => L + (i * (W - L - R)) / (pts.length - 1);
  const y = (v) => T + ((hi - v) * (H - T - B)) / (hi - lo);
  const line = pts.map((p, i) => `${i ? "L" : "M"}${x(i).toFixed(1)},${y(p.min_price_cents).toFixed(1)}`).join("");
  const ticks = [0, 0.5, 1].map((f) => lo + (hi - lo) * f);
  const fmtDay = (d) => d.slice(8, 10) + "/" + d.slice(5, 7);
  const minIdx = vals.indexOf(Math.min(...vals));
  const xLabels = [0, Math.floor((pts.length - 1) / 2), pts.length - 1];
  return `<svg viewBox="0 0 ${W} ${H}" role="img" aria-label="Histórico do menor preço: mínimo ${money(vals[minIdx])} em ${fmtDay(pts[minIdx].day)}">
    ${ticks.map((v) => `<line class="grid-line" x1="${L}" x2="${W - R}" y1="${y(v)}" y2="${y(v)}"/>
      <text class="axis" x="${L - 8}" y="${y(v) + 4}" text-anchor="end">${money(Math.round(v))}</text>`).join("")}
    ${xLabels.map((i) => `<text class="axis" x="${x(i)}" y="${H - 8}" text-anchor="${i === 0 ? "start" : i === pts.length - 1 ? "end" : "middle"}">${fmtDay(pts[i].day)}</text>`).join("")}
    <path class="area" d="${line}L${x(pts.length - 1)},${H - B}L${L},${H - B}Z"/>
    <path class="line" d="${line}"/>
    <circle class="dot" cx="${x(minIdx)}" cy="${y(vals[minIdx])}" r="4"><title>Menor: ${money(vals[minIdx])} em ${fmtDay(pts[minIdx].day)}</title></circle>
    <circle class="dot" cx="${x(pts.length - 1)}" cy="${y(vals.at(-1))}" r="4"><title>Hoje: ${money(vals.at(-1))}</title></circle>
  </svg>
  <p class="small muted">Menor preço no período: <strong>${money(vals[minIdx])}</strong> em ${fmtDay(pts[minIdx].day)} · hoje: <strong>${money(vals.at(-1))}</strong></p>`;
}

function authPage(mode) {
  const isLogin = mode === "login";
  document.title = `${isLogin ? "Entrar" : "Criar conta"} — Comparaí`;
  const next = new URLSearchParams(location.search).get("next");
  const safeNext = next && next.startsWith("/") && !next.startsWith("//") ? next : "/conta";
  app.innerHTML = `
    <div class="card auth">
      <h1>${isLogin ? "Entrar" : "Criar conta"}</h1>
      <form id="auth-form">
        ${isLogin ? "" : `<label>Nome<input type="text" name="name" required maxlength="120" autocomplete="name"></label>`}
        <label>Email<input type="email" name="email" required autocomplete="email"></label>
        <label>Senha<input type="password" name="password" required minlength="8" maxlength="72" autocomplete="${isLogin ? "current-password" : "new-password"}"></label>
        <p class="error" id="auth-error" role="alert"></p>
        <button type="submit">${isLogin ? "Entrar" : "Criar conta"}</button>
      </form>
      <p class="small muted">${isLogin ? `Ainda não tem conta? <a href="/cadastro" data-link>Cadastre-se</a>` : `Já tem conta? <a href="/entrar" data-link>Entrar</a>`}</p>
    </div>`;
  document.getElementById("auth-form").addEventListener("submit", async (e) => {
    e.preventDefault();
    const btn = e.target.querySelector("button");
    btn.disabled = true;
    try {
      const body = Object.fromEntries(new FormData(e.target));
      const res = await api(`/auth/${isLogin ? "login" : "register"}`, { method: "POST", body });
      setSession({ token: res.token, user: res.user });
      navigate(safeNext, { replace: true });
    } catch (err) {
      document.getElementById("auth-error").textContent = err.message;
      btn.disabled = false;
    }
  });
}

async function account() {
  if (!session) return navigate("/entrar?next=/conta", { replace: true });
  document.title = "Minha conta — Comparaí";
  const [favs, alerts, notes] = await Promise.all([
    api("/me/favorites", { auth: true }),
    api("/me/alerts", { auth: true }),
    api("/me/notifications", { auth: true }),
  ]);
  const unread = notes.filter((n) => !n.read_at).length;
  app.innerHTML = `
    <h1>Minha conta</h1>
    <section class="section">
      <div class="toolbar"><h2 style="margin:0">Notificações ${unread ? `<span class="badge good">${unread} nova${unread > 1 ? "s" : ""}</span>` : ""}</h2>
        ${unread ? `<button class="ghost" id="read-all" type="button">Marcar como lidas</button>` : ""}</div>
      <div class="card">${notes.length ? notes.map((n) => `
        <div class="list-row">
          <div class="grow">${n.product_slug ? `<a href="/produto/${encodeURIComponent(n.product_slug)}" data-link>${esc(n.message)}</a>` : esc(n.message)}
            <div class="small muted">${new Date(n.created_at).toLocaleString("pt-BR")}</div></div>
          ${n.read_at ? "" : `<span class="badge good">nova</span>`}
        </div>`).join("") : `<div class="empty">Nenhuma notificação. Crie um alerta em qualquer produto.</div>`}</div>
    </section>
    <section class="section">
      <h2>Alertas de preço</h2>
      <div class="card">${alerts.length ? alerts.map((a) => `
        <div class="list-row">
          <div class="grow"><a href="/produto/${encodeURIComponent(a.product_slug)}" data-link>${esc(a.product_title)}</a>
            <div class="small muted">alvo ${money(a.target_price_cents)} · agora ${money(a.current_price_cents)}</div></div>
          ${a.active ? `<span class="badge neutral">ativo</span>` : `<span class="badge good">disparado</span>`}
          <button class="ghost" type="button" data-del-alert="${a.id}">Excluir</button>
        </div>`).join("") : `<div class="empty">Você ainda não criou alertas.</div>`}</div>
    </section>
    <section class="section">
      <h2>Favoritos</h2>
      ${grid(favs)}
    </section>`;
  document.getElementById("read-all")?.addEventListener("click", async () => {
    await api("/me/notifications/read", { method: "POST", auth: true });
    account();
  });
  app.querySelectorAll("[data-del-alert]").forEach((b) =>
    b.addEventListener("click", async () => {
      await api(`/me/alerts/${b.dataset.delAlert}`, { method: "DELETE", auth: true });
      toast("Alerta excluído");
      account();
    }));
}

// ---------- busca + autocomplete ----------
const input = document.getElementById("search-input");
const suggestEl = document.getElementById("suggest");
let suggestTimer, suggestSeq = 0;

function hideSuggest() { suggestEl.hidden = true; suggestEl.innerHTML = ""; }

document.getElementById("search-form").addEventListener("submit", (e) => {
  e.preventDefault();
  const q = input.value.trim();
  if (q) navigate(`/busca?q=${encodeURIComponent(q)}`);
});

input.addEventListener("input", () => {
  clearTimeout(suggestTimer);
  const q = input.value.trim();
  if (q.length < 2) return hideSuggest();
  suggestTimer = setTimeout(async () => {
    const seq = ++suggestSeq;
    const items = await api(`/suggest?q=${encodeURIComponent(q)}&limit=6`).catch(() => []);
    if (seq !== suggestSeq) return; // resposta antiga
    if (!items.length) return hideSuggest();
    suggestEl.innerHTML = items.map((p) => `<li><a href="/produto/${encodeURIComponent(p.slug)}" data-link role="option">
      <span>${esc(p.title)}</span><strong>${money(p.min_price_cents)}</strong></a></li>`).join("");
    suggestEl.hidden = false;
  }, 180);
});

input.addEventListener("keydown", (e) => {
  const links = [...suggestEl.querySelectorAll("a")];
  if (suggestEl.hidden || !links.length) return;
  const cur = links.findIndex((a) => a.getAttribute("aria-selected") === "true");
  if (e.key === "ArrowDown" || e.key === "ArrowUp") {
    e.preventDefault();
    const next = (cur + (e.key === "ArrowDown" ? 1 : -1) + links.length) % links.length;
    links.forEach((a, i) => a.setAttribute("aria-selected", String(i === next)));
  } else if (e.key === "Enter" && cur >= 0) {
    e.preventDefault();
    navigate(links[cur].getAttribute("href"));
  } else if (e.key === "Escape") {
    hideSuggest();
  }
});
document.addEventListener("click", (e) => { if (!e.target.closest(".search")) hideSuggest(); });

renderNav();
render();
