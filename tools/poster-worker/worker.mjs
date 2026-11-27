const tokenField = "TMDB_TOKEN";
const tmdbAPI = "https://api.themoviedb.org/3/";
const imageBase = "https://image.tmdb.org/t/p/w500";
const maxResultBytes = 1 << 20;
const maxMatchesPerKind = 20;
const maxTitleLength = 200;
const lookupTimeoutMs = 8_000;
const maxConcurrentLookups = 8;
const memoryEntries = 64;
const imagePath = /^\/[a-z0-9_-]+\.(jpe?g|png|webp)$/i;

const hot = new Map();
const pending = new Map();
let upstreamPauseUntil = 0;

function json(body, status = 200, headers = {}) {
  return Response.json(body, {
    status,
    headers: {
      "Cache-Control": "no-store",
      "X-Content-Type-Options": "nosniff",
      ...headers,
    },
  });
}

function parseQuery(url) {
  const params = url.searchParams;
  const allowed = new Set(["title", "year", "kind"]);
  for (const key of params.keys()) {
    if (!allowed.delete(key)) return null;
  }
  const rawTitle = params.get("title") ?? "";
  if (rawTitle.length > maxTitleLength || /[\x00-\x1f\x7f]/.test(rawTitle)) return null;
  const title = rawTitle.trim().replace(/\s+/gu, " ");
  const year = params.get("year") ?? "";
  const kind = params.get("kind") ?? "";
  if (!title || !/[\p{L}\p{N}]/u.test(title)) return null;
  if (year && !/^[1-9]\d{3}$/.test(year)) return null;
  if (kind && kind !== "movie" && kind !== "tv") return null;
  return { title, year, kind };
}

async function readResult(response) {
  const declared = Number(response.headers.get("Content-Length"));
  if (!response.body || declared > maxResultBytes) {
    await response.body?.cancel();
    throw new Error("invalid response length");
  }
  const reader = response.body.getReader();
  const chunks = [];
  let bytes = 0;
  try {
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      bytes += value.byteLength;
      if (bytes > maxResultBytes) throw new Error("response too large");
      chunks.push(value);
    }
  } catch (error) {
    try { await reader.cancel(); } catch {}
    throw error;
  } finally {
    reader.releaseLock();
  }
  const body = new Uint8Array(bytes);
  let offset = 0;
  for (const chunk of chunks) {
    body.set(chunk, offset);
    offset += chunk.byteLength;
  }
  return JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(body));
}

function matchItem(item, kind) {
  if (!item || !Number.isSafeInteger(item.id) || item.id <= 0 || item.adult === true) return null;
  const bounded = (value) => typeof value === "string" ? value.trim().slice(0, 400) : "";
  const isTV = kind === "tv";
  const originalTitle = bounded(isTV ? item.original_name : item.original_title);
  const title = bounded(isTV ? item.name : item.title) || originalTitle;
  if (!title) return null;
  const date = isTV ? item.first_air_date : item.release_date;
  const year = typeof date === "string" && /^[1-9]\d{3}-\d{2}-\d{2}$/.test(date) ? date.slice(0, 4) : "";
  const path = item.poster_path;
  const posterURL = typeof path === "string" && path.length <= 200 && imagePath.test(path) ? imageBase + path : "";
  return { id: item.id, kind, title, original_title: originalTitle, year, poster_url: posterURL };
}

function retryDelay(value) {
  if (value && /^\d{1,9}$/.test(value)) return Math.max(1, Number(value));
  const date = value && Date.parse(value);
  return date && date > Date.now() ? Math.ceil((date - Date.now()) / 1_000) : 5;
}

async function fetchKind(query, kind, token) {
  const url = new URL("search/" + kind, tmdbAPI);
  url.searchParams.set("query", query.title);
  url.searchParams.set("language", "zh-CN");
  url.searchParams.set("include_adult", "false");
  if (query.year) url.searchParams.set(kind === "tv" ? "first_air_date_year" : "year", query.year);

  let response;
  try {
    response = await fetch(url, {
      headers: { Accept: "application/json", Authorization: "Bearer " + token, "User-Agent": "Xiaomu-Poster/1.0" },
      redirect: "manual",
      signal: AbortSignal.timeout(lookupTimeoutMs),
    });
  } catch (error) {
    const timeout = error?.name === "TimeoutError" || error?.name === "AbortError";
    return { status: timeout ? 504 : 502, error: timeout ? "upstream_timeout" : "upstream_unreachable" };
  }
  if (!response.ok) {
    try { await response.body?.cancel(); } catch {}
    const status = response.status;
    if (status === 429) {
      const delay = retryDelay(response.headers.get("Retry-After"));
      upstreamPauseUntil = Math.max(upstreamPauseUntil, Date.now() + delay * 1_000);
      return { status: 429, error: "upstream_busy", retry: delay };
    }
    const error = status === 401 || status === 403 ? "upstream_not_authorized" : "upstream_unavailable";
    return { status: 502, error, upstreamStatus: status };
  }
  try {
    const result = await readResult(response);
    if (!Array.isArray(result?.results)) return { status: 502, error: "upstream_invalid" };
    const matches = result.results.slice(0, maxMatchesPerKind)
      .map(item => matchItem(item, kind)).filter(Boolean);
    return { status: 200, matches };
  } catch {
    return { status: 502, error: "upstream_invalid" };
  }
}

async function lookup(query, token) {
  const kinds = query.kind ? [query.kind] : ["movie", "tv"];
  const results = await Promise.all(kinds.map(kind => fetchKind(query, kind, token)));
  return results.find(result => result.status === 429)
    ?? results.find(result => result.status !== 200)
    ?? { status: 200, matches: results.flatMap(result => result.matches) };
}

function cacheKey(url, query) {
  const key = new URL("/__poster_cache/v1", url.origin);
  key.search = new URLSearchParams(query).toString();
  return key.toString();
}

function remember(key, matches) {
  if (hot.size >= memoryEntries) hot.delete(hot.keys().next().value);
  hot.set(key, { matches, expires: Date.now() + 60_000 });
}

function failure(result) {
  const headers = result.retry ? { "Retry-After": String(result.retry) } : {};
  const body = { error: result.error };
  if (result.upstreamStatus) body.upstream_status = result.upstreamStatus;
  return json(body, result.status, headers);
}

export default {
  async fetch(request, env, ctx) {
    const url = new URL(request.url);
    if (url.pathname !== "/v1/posters" && url.pathname !== "/healthz") {
      return json({ error: "not_found" }, 404);
    }
    if (request.method !== "GET") {
      return json({ error: "method_not_allowed" }, 405, { Allow: "GET" });
    }
    const token = env[tokenField]?.trim() ?? "";
    if (url.pathname === "/healthz") return json({ configured: token !== "" });

    if (url.search.length > 3_000) return json({ error: "invalid_query" }, 400);
    const query = parseQuery(url);
    if (!query) return json({ error: "invalid_query" }, 400);
    if (!token) return json({ error: "not_configured" }, 503);
    if (!/^[!-~]+$/.test(token)) return json({ error: "invalid_secret_format" }, 503);

    const key = cacheKey(url, query);
    const recent = hot.get(key);
    if (recent && recent.expires > Date.now()) return json({ matches: recent.matches });
    if (recent) hot.delete(key);
    const cache = globalThis.caches?.default;
    try {
      const cached = await cache?.match(key);
      if (cached) return cached;
    } catch {
      // A cache miss or unavailable cache still permits an upstream lookup.
    }
    const running = pending.get(key);
    if (running) {
      const result = await running;
      return result.status === 200 ? json({ matches: result.matches }) : failure(result);
    }
    const now = Date.now();
    if (upstreamPauseUntil > now) {
      return failure({ status: 429, error: "upstream_busy", retry: Math.ceil((upstreamPauseUntil - now) / 1_000) });
    }
    if (pending.size >= maxConcurrentLookups) {
      return failure({ status: 429, error: "service_busy", retry: 3 });
    }

    const job = lookup(query, token);
    pending.set(key, job);
    try {
      const result = await job;
      if (result.status !== 200) return failure(result);
      remember(key, result.matches);
      if (cache) {
        const seconds = result.matches.length ? 86_400 : 300;
        const cached = json({ matches: result.matches }, 200, {
          "Cache-Control": "public, max-age=" + seconds,
        });
        ctx.waitUntil(cache.put(key, cached).catch(() => {}));
      }
      return json({ matches: result.matches });
    } finally {
      pending.delete(key);
    }
  },
};
