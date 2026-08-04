import assert from "node:assert/strict";
import test from "node:test";

async function render() {
  const workerUrl = new URL("../dist/server/index.js", import.meta.url);
  workerUrl.searchParams.set("test", `${process.pid}-${Date.now()}`);
  const { default: worker } = await import(workerUrl.href);
  return worker.fetch(
    new Request("http://localhost/", { headers: { accept: "text/html" } }),
    { ASSETS: { fetch: async () => new Response("Not found", { status: 404 }) } },
    { waitUntil() {}, passThroughOnException() {} },
  );
}

test("server-renders the momentum decision console", async () => {
  const response = await render();
  assert.equal(response.status, 200);
  assert.match(response.headers.get("content-type") ?? "", /^text\/html\b/i);
  const html = await response.text();
  assert.match(html, /<title>Momentum Intelligence<\/title>/i);
  assert.match(html, /Dynamic candidate tape/);
  assert.match(html, /AH BOUNDARY TOP 3/);
  assert.match(html, /15:55 ET NEWS GATE/);
  assert.match(html, /SHADOW ONLY/);
  assert.match(html, /CONTINUOUS SCANNER/);
  assert.match(html, /SPIKE WATCH/);
  assert.match(html, />catalysts</i);
  assert.match(html, /MODEL SEED → LIVE CONFIRMATION/);
  assert.match(html, /SCAN → RANK → WATCH BOOK → EXECUTE/);
  assert.match(
    html,
    /WATCHING → PULLBACK → ORDER PENDING → IN POSITION → EXIT PENDING/,
  );
  assert.match(html, /DECISION RAIL/);
  assert.match(html, /OPK/);
  assert.match(html, /TOP OF BOOK/);
  assert.doesNotMatch(html, /codex-preview|react-loading-skeleton/i);
});
