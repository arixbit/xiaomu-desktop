import assert from "node:assert/strict";
import { test } from "node:test";

const env = { TMDB_TOKEN: "fixture-token" };
const ctx = { waitUntil() {} };
const request = (path, init) => new Request("https://example.com" + path, init);

test("health reports configuration without sending a request", async t => {
  const { default: worker } = await import("./worker.mjs?health");
  t.mock.method(globalThis, "fetch", () => assert.fail("unexpected TMDB fetch"));

  const disabled = await worker.fetch(request("/healthz"), {}, ctx);
  assert.equal(disabled.status, 200);
  assert.deepEqual(await disabled.json(), { configured: false });
  const enabled = await worker.fetch(request("/healthz"), env, ctx);
  assert.deepEqual(await enabled.json(), { configured: true });
  assert.equal(enabled.headers.get("Cache-Control"), "no-store");

  const unavailable = await worker.fetch(request("/v1/posters?title=Sample"), {}, ctx);
  assert.equal(unavailable.status, 503);
  assert.deepEqual(await unavailable.json(), { error: "not_configured" });
});

test("only GET searches with validated parameters are accepted", async t => {
  const { default: worker } = await import("./worker.mjs?validation");
  t.mock.method(globalThis, "fetch", () => assert.fail("unexpected TMDB fetch"));

  for (const path of [
    "/v1/posters",
    "/v1/posters?title=%20",
    "/v1/posters?title=Sample%0A",
    "/v1/posters?title=Sample&title=Other",
    "/v1/posters?title=Sample&year=202",
    "/v1/posters?title=Sample&kind=person",
    "/v1/posters?title=Sample&url=https://example.com",
    "/v1/posters?title=" + "a".repeat(201),
  ]) {
    assert.equal((await worker.fetch(request(path), env, ctx)).status, 400, path);
  }
  assert.equal((await worker.fetch(request("/v1/anything"), env, ctx)).status, 404);
  const badMethod = await worker.fetch(request("/v1/posters", { method: "POST" }), env, ctx);
  assert.equal(badMethod.status, 405);
  assert.equal(badMethod.headers.get("Allow"), "GET");
});

test("only fixed TMDB routes receive credentials; response is bounded and sanitized", async t => {
  const { default: worker } = await import("./worker.mjs?sanitize");
  const seen = new Map();
  t.mock.method(globalThis, "fetch", async (url, options) => {
    assert.equal(url.origin, "https://api.themoviedb.org");
    assert.equal(url.searchParams.get("query"), "示例剧");
    assert.equal(url.searchParams.get("language"), "zh-CN");
    assert.equal(url.searchParams.get("include_adult"), "false");
    assert.equal(options.headers.Authorization, "Bearer fixture-token");
    assert.equal(options.redirect, "manual");
    assert.ok(options.signal);
    seen.set(url.pathname, url.searchParams);
    if (url.pathname.endsWith("/movie")) {
      return Response.json({ results: [
        { id: 1, title: "电影", original_title: "Sample Film", release_date: "2024-06-07", poster_path: "//example.com/file.jpg" },
        { id: 2, title: "成人条目", adult: true, poster_path: "/valid.png" },
      ] });
    }
    const payload = new TextEncoder().encode(JSON.stringify({ results: [
      { id: 3, name: "示例剧", original_name: "Sample Series", first_air_date: "2024-01-02", poster_path: "/valid.png" },
      { id: 4, name: "无图片", original_name: "Another Series", first_air_date: "2024-01-02" },
      ...Array.from({ length: 45 }, (_, index) => ({ id: 100 + index, name: "其他" })),
    ] }));
    return new Response(new ReadableStream({
      start(controller) {
        const split = payload.findIndex(byte => byte >= 128) + 1;
        controller.enqueue(payload.subarray(0, split));
        controller.enqueue(payload.subarray(split));
        controller.close();
      },
    }));
  });
  const response = await worker.fetch(request("/v1/posters?title=%E7%A4%BA%E4%BE%8B%E5%89%A7&year=2024"), env, ctx);
  assert.equal(response.status, 200);
  const body = await response.json();
  assert.equal(seen.size, 2);
  assert.equal(seen.get("/3/search/movie").get("year"), "2024");
  assert.equal(seen.get("/3/search/tv").get("first_air_date_year"), "2024");
  assert.equal(body.matches.length, 21);
  assert.deepEqual(body.matches.slice(0, 3), [
    { id: 1, kind: "movie", title: "电影", original_title: "Sample Film", year: "2024", poster_url: "" },
    { id: 3, kind: "tv", title: "示例剧", original_title: "Sample Series", year: "2024", poster_url: "https://image.tmdb.org/t/p/w500/valid.png" },
    { id: 4, kind: "tv", title: "无图片", original_title: "Another Series", year: "2024", poster_url: "" },
  ]);
});

test("identical requests share a lookup and misses receive a short cache lifetime", async t => {
  const { default: worker } = await import("./worker.mjs?cache");
  const entries = new Map();
  const cache = {
    match: async key => entries.get(key)?.clone(),
    put: async (key, response) => entries.set(key, response),
  };
  const oldCaches = Object.getOwnPropertyDescriptor(globalThis, "caches");
  Object.defineProperty(globalThis, "caches", { configurable: true, value: { default: cache } });
  t.after(() => {
    if (oldCaches) Object.defineProperty(globalThis, "caches", oldCaches);
    else delete globalThis.caches;
  });
  let calls = 0;
  t.mock.method(globalThis, "fetch", async () => {
    calls++;
    return Response.json({ results: [] });
  });
  const one = "/v1/posters?kind=tv&title=%20A%20%20Series%20";
  const two = "/v1/posters?title=A%20Series&kind=tv";
  const lookup = [one, two].map(path => worker.fetch(request(path), env, ctx));
  for (const response of await Promise.all(lookup)) {
    assert.deepEqual(await response.json(), { matches: [] });
  }
  assert.deepEqual(await (await worker.fetch(request(two), env, ctx)).json(), { matches: [] });
  assert.equal(calls, 1);
  assert.equal(entries.size, 1);
  assert.equal(entries.values().next().value.headers.get("Cache-Control"), "public, max-age=300");
});

test("upstream limits stop new lookups and partial responses are never returned", async t => {
  const { default: worker } = await import("./worker.mjs?limit");
  let calls = 0;
  t.mock.method(globalThis, "fetch", async url => {
    calls++;
    if (url.pathname.endsWith("/tv")) return new Response(null, { status: 429, headers: { "Retry-After": "23" } });
    return Response.json({ results: [{ id: 9, title: "Sample Film" }] });
  });
  const limited = await worker.fetch(request("/v1/posters?title=Sample"), env, ctx);
  assert.equal(limited.status, 429);
  assert.equal(limited.headers.get("Retry-After"), "23");
  assert.deepEqual(await limited.json(), { error: "upstream_busy" });
  const paused = await worker.fetch(request("/v1/posters?title=Another&kind=movie"), env, ctx);
  assert.equal(paused.status, 429);
  assert.ok(Number(paused.headers.get("Retry-After")) > 0);
  assert.equal(calls, 2);
});

test("upstream authentication and oversized responses fail without exposing upstream content", async t => {
  const { default: worker } = await import("./worker.mjs?errors");
  let calls = 0;
  t.mock.method(globalThis, "fetch", async () => {
    calls++;
    if (calls === 1) return new Response("private upstream content", { status: 401 });
    return new Response("x".repeat((1 << 20) + 1));
  });
  const ask = request("/v1/posters?title=Sample&kind=movie");
  for (const result of [{ error: "upstream_not_authorized", upstream_status: 401 }, { error: "upstream_invalid" }]) {
    const response = await worker.fetch(ask, env, ctx);
    assert.equal(response.status, 502);
    assert.deepEqual(await response.json(), result);
  }
});
