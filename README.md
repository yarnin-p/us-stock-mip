# Momentum Intelligence Platform (MIP)

AI Trading Copilot สำหรับ momentum trades ตาม PRD v2 โดย quantitative engine
ทำงานได้โดยไม่ต้องมี LLM และ LLM เป็น optional explanation layer เท่านั้น

## สิ่งที่ทำแล้ว

- Go repository แบบ right-sized: `cmd/mip` เป็น composition root และ business code อยู่ใน `internal/`
- PostgreSQL migrations สำหรับ `stocks`, `daily_prices`, `intraday_prices`, `news`, `sec_filings`, `trades`
- `feature_snapshots` ที่แยกตาม ticker, date และ calculator periods เพื่อไม่ให้ config ใหม่ทับผลย้อนหลัง
- effective-dated float history และ model cutoff/config binding เพื่อกัน point-in-time leakage
- Massive adapter สำหรับ ticker overview, free float และ historical aggregate bars
- ETL สำหรับ daily/minute OHLCV พร้อม upsert
- Feature Engine สำหรับ price, volume, momentum, catalyst และ risk features พร้อม nullable values เมื่อข้อมูลต้นทางไม่พอ
- Backtest แบบ next-bar open พร้อม stop/target, slippage, commission, win rate, profit factor, drawdown และ Sharpe
- Probability ranker แบบ logistic baseline และ boosted-tree candidates
  `LightGBM`, `XGBoost`, `CatBoost` ที่ train/score ใน Go และ version artifact ใน PostgreSQL
- Massive News/Splits และ SEC primary-document ingestion พร้อม deterministic scoring และ LLM analysis
- Alpaca Benzinga News แบบ real-time ผ่าน WebSocket พร้อม REST reconciliation, cross-provider deduplication และ source-latency health
- LLM workflows สำหรับ news catalyst, SEC dilution, ranking explanation และ trading-journal summary
- Webull HMAC signing, token create/check/refresh, REST snapshot polling และ MQTT protobuf streaming
- Daily opening Top 5 สำหรับช่วงวันซื้อขายจาก market-wide Massive summary
  พร้อม point-in-time cutoff, PRD v2 component scores และ score coverage
- CLI `etl`, `features`, `backtest`, `train`, `rank`, `opening-list`,
  `intelligence`, `llm`, `webull-token` และ `scan`
- Docker Compose สำหรับ PostgreSQL และ migration runner
- always-running Thailand-time scheduler พร้อม retry/status, SSE dashboard updates,
  system health, score timeline, rule alerts และ position lifecycle/review
- Unit tests ของ Massive client, pagination/security checks, config, ETL และ Feature Engine; PostgreSQL integration test แยกด้วย build tag

## เริ่มใช้งาน

ต้องมี Go 1.26+, Docker, Docker Compose และ Python 3.12 สำหรับ model families

```bash
cp .env.example .env
# เติม MASSIVE_API_KEY และเปลี่ยน POSTGRES_PASSWORD ใน .env

python3.12 -m venv .venv
.venv/bin/python -m pip install -r requirements-ml.txt

make db-up
make migrate-up
set -a && source .env && set +a
go run ./cmd/mip etl \
  --tickers AAPL,MSFT \
  --timespan day \
  --from 2026-01-01 \
  --to 2026-01-31
```

สำหรับ minute bars:

```bash
go run ./cmd/mip etl \
  --tickers AAPL \
  --timespan minute \
  --multiplier 1 \
  --from 2026-01-02 \
  --to 2026-01-02
```

หนึ่ง minute job รับช่วงเวลาไม่เกิน 31 วันเพื่อจำกัด memory; แบ่งช่วงยาวเป็นหลาย job ได้

หลังโหลด daily bars และ minute bars ของวันที่ต้องการแล้ว สร้าง feature snapshots ด้วย:

```bash
go run ./cmd/mip features \
  --tickers AAPL,MSFT \
  --date 2026-01-31 \
  --rvol-period 20 \
  --ema-period 9 \
  --breakout-period 20
```

คำสั่ง `features` ใช้เพียง `DATABASE_URL`; ไม่ต้องใช้ Massive API key เพราะอ่านจากข้อมูลที่ ingest แล้ว

## สูตร Feature Engine

ค่าร้อยละเก็บเป็น ratio เช่น `0.25` หมายถึง 25%

| Feature | Formula |
|---|---|
| Gap | `current open / previous close - 1` |
| Premarket change | `latest premarket close / previous close - 1` |
| After-hours change | `latest after-hours close / current close - 1` |
| 1-day return | `current close / previous close - 1` |
| Relative volume | `current volume / mean(prior N daily volumes)` |
| Volume spike | `current volume / previous daily volume` |
| Float rotation | `current volume / float shares` |
| EMA | standard EMA with `alpha = 2 / (period + 1)`, an SMA seed, and a fixed `3 × period` warm-up window |
| VWAP distance | `current close / current VWAP - 1` |
| Breakout strength | `current close / maximum prior high over N days - 1` |

Relative volume และ breakout ใช้ประวัติเท่าที่มีได้ ส่วน EMA จะเป็น `NULL` จนมี close ครบตาม period ค่า EMA ใช้ warm-up ที่ผูกกับ EMA period โดยตรง จึงไม่เปลี่ยนตาม RVOL/breakout config และทุก snapshot เก็บ calculator version เพื่อป้องกันสูตรรุ่นใหม่ทับผลรุ่นเก่า

ค่า catalyst/risk (`news`, FDA, M&A, theme, ATM, offering, reverse split) มาจาก versioned intelligence snapshot ล่าสุดที่มีเวลาไม่เกินวัน feature นั้น หากยังไม่เคย ingest จะเก็บเป็น `NULL` แทนการเดาค่า

## Sprint 3–6 workflows

Backtest จะสร้าง signal จาก feature snapshot ที่ relative volume ผ่าน threshold แล้วเข้า order ที่ open ของ trading bar ถัดไป เพื่อไม่ใช้ข้อมูลอนาคต:

```bash
go run ./cmd/mip backtest \
  --ticker AAPL --from 2025-01-01 --to 2025-12-31 \
  --min-rvol 2 --holding-bars 5 --stop 0.10 --target 0.50
```

Train probability model และ rank หุ้นของวันหนึ่ง:

```bash
# Efficient point-in-time feature backfill used by the daily learner
go run ./cmd/mip features-backfill \
  --from 2025-01-01 --to 2025-12-31

go run ./cmd/mip train \
  --name runner-baseline --from 2024-01-01 --to 2025-12-31 \
  --validation-fraction .20 --min-validation-samples 50
go run ./cmd/mip train \
  --name runner-lightgbm --algorithm lightgbm_v1 \
  --boost-rounds 96 --from 2024-01-01 --to 2025-12-31
go run ./cmd/mip train \
  --name runner-xgboost --algorithm xgboost_v1 \
  --boost-rounds 96 --from 2024-01-01 --to 2025-12-31
go run ./cmd/mip train \
  --name runner-catboost --algorithm catboost_v1 \
  --boost-rounds 96 --from 2024-01-01 --to 2025-12-31
go run ./cmd/mip rank --name runner-baseline --date 2026-01-31
```

Logistic baseline รันใน Go ส่วนสาม model families เรียก package จริงที่ pin ไว้ใน
`requirements-ml.txt` ผ่าน bounded JSON bridge: LightGBM 4.7, XGBoost 3.3 และ
CatBoost 1.2.10 ทั้ง training และ inference เก็บ native model artifact, metrics,
training window, feature names และ calculator config แบบ versioned
ใน PostgreSQL Feature ของวัน `t`
ทำนาย intraday runner ของ session ถัดไปหรือผลตอบแทน close วันที่ 5 และต้องมี
future horizon ครบ 5 trading bars จึงไม่เอาข้อมูลอนาคตมาปนใน input

คำสั่ง `train` แยกวันล่าสุด 20% เป็น validation holdout โดยไม่ให้วันเดียวกัน
กระจายอยู่ทั้งสองฝั่ง และเก็บ accuracy, log loss, Brier score, positive rate
กับ top-decile precision รุ่นใหม่เริ่มเป็น `challenger`; ระบบจะเปลี่ยน
`champion` ก็ต่อเมื่อชนะ model เดิมบน validation dates ชุดเดียวกันตาม promotion
policy เท่านั้น ทุกการ promote/reject ถูกเก็บใน `model_learning_runs`

Scheduler sync Massive grouped daily ของ session ที่จบแล้วเวลา 13:20 ICT
และรัน feature backfill + retraining เวลา 13:30 ICT ส่วน live discovery
ไม่ได้ล็อกรายชื่อจาก pre-market หรือรอเวลาเปิด Regular ระบบจะเรียก Webull
screener, refresh snapshot, ประเมิน scanner และสร้าง dynamic ranking ใหม่ทุก
`CONTINUOUS_SCAN_INTERVAL` ตลอดทุก tradable session รายชื่อ Top N จึงเพิ่ม
ลดหรือสลับอันดับได้ตามข้อมูลล่าสุด

Probability ล่าสุดที่ผ่าน point-in-time cutoff จะถูกรวมกับ momentum/volume
score โดยอัตโนมัติ ค่าเริ่มต้น `SCANNER_MIN_CHANGE=0` และ
`SCANNER_MIN_VOLUME=0` ช่วยไม่ทิ้ง candidate เร็วเกินไป แต่ก่อนส่งคำสั่งยัง
ต้องผ่าน QUOTE+TICK freshness, spread, pullback/reclaim และ risk engine ครบ

`TRADING_ALLOWED_SESSIONS=OVERNIGHT,PRE_MARKET,REGULAR,AFTER_HOURS`
เปิดให้ strategy ทำงานต่อเนื่องทุกช่วงที่ซื้อขายได้ คำสั่งใน pre-market,
Regular และ after-hours ใช้ Webull `support_trading_session=ALL`; overnight
ใช้ `support_trading_session=NIGHT` โดย execution service จะเลือกให้ตามเวลา
ส่งคำสั่งจริง

## Daily Opening Top 5 (ไม่ใช้ LLM)

สร้างรายชื่อหุ้นเมื่อ official opening print ของแต่ละวันซื้อขายพร้อมใช้งาน:

```bash
# วันนี้ตามเวลา America/New_York
go run ./cmd/mip opening-list

# เปิด process ค้างไว้ให้สร้างรายชื่ออัตโนมัติทุก trading day หลัง 09:30 ET
go run ./cmd/mip opening-list --watch

# หนึ่งวัน หรือ backfill หลายวัน
go run ./cmd/mip opening-list --date 2026-07-22
go run ./cmd/mip opening-list \
  --from 2026-07-20 --to 2026-07-22 \
  --limit 5 --format table

# อ่านเฉพาะ market history ที่ sync แล้ว; รองรับ table, csv และ json
go run ./cmd/mip opening-list \
  --from 2026-07-20 --to 2026-07-22 \
  --sync=false --format csv
```

ค่า default คือราคาเปิด `$1–$20`, opening gap อย่างน้อย `5%`, prior average
dollar volume อย่างน้อย `$1M`, lookback 20 sessions และ Top 5 ต่อวัน คำสั่งจะ
sync unadjusted market-wide grouped daily data และ point-in-time common-stock
universe (`type=CS`) ที่ขาดจาก Massive โดยคุม rate limit ทุก HTTP request
วันปัจจุบันจะ enrich reference data ของ preliminary Top 25 ด้วย timestamp ที่
สังเกตเห็นจริง ส่วน historical backfill จะไม่เอา reference data ที่เพิ่งรู้วันนี้
ไปย้อนใส่เวลาเปิดตลาดในอดีต จากนั้นบันทึก run แบบ append-only, criteria,
screened candidate pool ทั้งหมด, raw inputs, component scores และ Top 5 ใน PostgreSQL

Current-day bar ใช้เฉพาะ `open`; momentum, prior RVOL และ liquidity ใช้เฉพาะ
session ที่จบและถูก mark `complete` ก่อนวันนั้น จึงไม่อ่าน high/low/close/volume
ของอนาคต Current session จะถูก refresh หลังปิดตลาดก่อนนำไปใช้เป็น history วันถัดไป
และ historical bars ใช้ unadjusted data เพื่อไม่ให้ split ที่ประกาศภายหลังย้อนกลับมา
เปลี่ยน opening decision เดิม คะแนนใช้ weight ตาม PRD v2:

| Component | Weight |
|---|---:|
| Momentum | 25% |
| Float | 20% |
| Catalyst | 20% |
| Volume/RVOL | 15% |
| Market Cap | 10% |
| Sector | 5% |
| Dilution Risk | 5% |

ถ้า point-in-time data บาง component ยังไม่มี ระบบจะไม่เดาค่าและให้น้ำหนักส่วนนั้น
เป็นศูนย์ พร้อมแสดง `COVERAGE` ชัดเจน ตัวอย่าง `40%` หมายถึง ranking รอบนั้นมี
Momentum + Volume/RVOL แล้ว แต่ยังไม่มี metadata/intelligence ที่รู้ได้ ณ cutoff
โดยเฉพาะ Sector score จะรับเฉพาะค่าที่คำนวณจาก sector universe แบบ point-in-time
ไม่อนุมานจาก candidate subset

Ingest ข่าว, split และ SEC filings แบบ point-in-time โดย CIK ต้องเป็นเลข 10 หลัก:

```bash
go run ./cmd/mip intelligence \
  --targets AAPL:0000320193,MSFT:0000789019 \
  --date 2026-01-31
```

เปิด LLM layer หลังตั้ง `LLM_API_KEY` ได้ 4 workflow ทุกผลลัพธ์เก็บ exact source,
source hash, provider endpoint, prompt version, model และ point-in-time timestamp
เพื่อ reproduce/audit (ฐานข้อมูลจึงต้องได้รับการป้องกันเหมือนข้อมูล journal):

```bash
go run ./cmd/mip llm --workflow news --ticker AAPL --date 2026-01-31
go run ./cmd/mip llm --workflow sec --ticker AAPL --date 2026-01-31
go run ./cmd/mip llm --workflow ranking --ticker AAPL --date 2026-01-31
go run ./cmd/mip llm --workflow journal --date 2026-01-31
```

LLM เป็น analysis/explanation layer เท่านั้น Prompt guardrail ห้ามส่งคำสั่งซื้อขาย
แนะนำ order หรือทำนายราคา และ SEC workflow จะดึง primary filing document จาก
`sec.gov` ด้วย `SEC_USER_AGENT` ก่อนวิเคราะห์

ครั้งแรกหรือเมื่อ token หมดอายุ ให้สร้าง/ตรวจ 2FA และเขียนไฟล์ mode `0600` แบบ
atomic; คำสั่ง refresh ใช้ token เดิม:

```bash
go run ./cmd/mip webull-token --action ensure
go run ./cmd/mip webull-token --action refresh
```

Realtime scanner ใช้ signed Webull stock snapshot; `--once` เหมาะกับ cron,
polling mode ทำงานต่อเนื่องเมื่อไม่ใส่ flag และ `--stream` ใช้ MQTT:

```bash
go run ./cmd/mip scan \
  --tickers AAPL,MSFT --min-price 1 --max-price 20 \
  --min-change 0.10 --min-volume 500000 --interval 5s
go run ./cmd/mip scan \
  --tickers AAPL,MSFT --stream \
  --min-price 1 --max-price 20 --min-change 0.10 --min-volume 500000
```

Streaming adapter ต่อ TLS MQTT, ใช้ App Key เป็น username, สร้าง session ID,
เรียก signed HTTP subscribe/unsubscribe, decode Webull Snapshot/QUOTE/TICK
protobuf และ subscribe ใหม่อัตโนมัติหลัง reconnect

Autonomous execution รับทั้ง order-book และ transaction tick จริงเข้า rolling
window แล้วตรวจจำนวน quote/tick, aggressive-buy volume, uptick ratio,
book pressure และ price velocity ก่อน entry พร้อมเก็บ top-of-book และ tick
history สำหรับ replay/backtest ในอนาคต Dashboard แสดงค่าหน้าต่างนี้ใน
Autonomous Strategy panel

Scanner จะดึง Runner Probability ล่าสุดที่ผ่าน point-in-time cutoff จาก `candidate_rankings` มารวมในคะแนน หากยังไม่มี ranking จะ fallback เป็น momentum/volume score

## ตรวจสอบคุณภาพ

```bash
make test
make test-race
make lint

# หลัง db-up + migrate-up และโหลด .env
make test-integration
```

## Environment variables

| Variable | Required | Default | Purpose |
|---|---:|---|---|
| `DATABASE_URL` | yes | - | PostgreSQL connection string |
| `MASSIVE_API_KEY` | ETL only | - | Massive REST authentication |
| `MASSIVE_BASE_URL` | no | `https://api.massive.com` | Override for tests/environment |
| `ALPACA_NEWS_ENABLED` | no | `false` | Enable Alpaca real-time news fast path |
| `APCA_API_KEY_ID` | Alpaca news | - | Alpaca paper/live API key ID; paper key works for Market Data |
| `APCA_API_SECRET_KEY` | Alpaca news | - | Alpaca API secret; keep only in `.env` |
| `ALPACA_DATA_BASE_URL` | no | `https://data.alpaca.markets` | Alpaca Market Data REST host (not the paper trading host) |
| `ALPACA_NEWS_STREAM_URL` | no | `wss://stream.data.alpaca.markets/v1beta1/news` | Alpaca real-time Benzinga News stream |
| `WEBULL_APP_KEY` | scanner | - | Webull OpenAPI application key |
| `WEBULL_APP_SECRET` | scanner | - | Used locally for request signatures; never sent as a header |
| `WEBULL_BASE_URL` | no | `https://api.webull.co.th` | Webull Thailand authentication and HTTP market-data API |
| `WEBULL_TRADING_BASE_URL` | no | `https://api.webull.co.th` | Webull Thailand trading/account API |
| `WEBULL_MQTT_URL` | no | `wss://data-api.webull.co.th:8883/mqtt` | Webull secure WebSocket MQTT broker |
| `WEBULL_ACCESS_TOKEN` | Thailand scanner | - | Reusable token after first-call 2FA |
| `WEBULL_ACCESS_TOKEN_FILE` | no | `.secrets/webull-token.txt` | Safer token-file alternative |
| `WEBULL_SIGNATURE_ALGORITHM` | no | `HMAC-SHA256` | `HMAC-SHA1` or `HMAC-SHA256` |
| `SEC_USER_AGENT` | intelligence | - | SEC-compliant app/contact identity |
| `SEC_BASE_URL` | no | `https://data.sec.gov` | SEC submissions host |
| `LLM_API_KEY` | LLM only | - | Responses-compatible provider credential |
| `LLM_BASE_URL` | no | `https://api.openai.com/v1` | Responses-compatible API base URL |
| `LLM_MODEL` | no | `gpt-5.6-luna` | Analysis model |
| `MIP_ML_PYTHON` | no | `.venv/bin/python` when present | Python runtime for native ML bridge |
| `HTTP_TIMEOUT` | no | `15s` | Per-request timeout |
| `DB_MAX_CONNS` | no | `10` | Maximum pool connections |
| `DB_MIN_CONNS` | no | `2` | Minimum pool connections |
| `LOG_LEVEL` | no | `info` | `debug`, `info`, `warn`, `error` |

## Data flow

```text
ETL CLI -> ETL runner -> Massive adapter -> PostgreSQL normalized tables
                                             |
Feature CLI -> Feature runner -> Calculator <-+
                              -> feature_snapshots
                                      |
Backtest CLI -> next-open simulator ----------+
Daily learning -> chronological holdout -> challenger/champion registry
Train/rank CLI -> logistic/boosted models ----+
Opening-list CLI -> Massive market summary -> point-in-time PRD v2 Top 5

Intelligence CLI -> Massive News/Splits + SEC -> intelligence snapshots
LLM CLI -> news/SEC/ranking/journal contexts -> audited analyses
Scan CLI -> Webull REST or MQTT snapshots -> scanner signals
Webull QUOTE + TICK -> order-flow window -> strategy/risk -> paper or broker
                     -> microstructure history + fills + PnL
```

The Massive aggregate adapter follows `next_url` pagination only when it remains on the configured Massive origin, never includes the API key in errors, and caps a request at 100,000 bars. Data writes use parameterized SQL, bounded batches, atomic transactions, and conflict-safe upserts.

Massive endpoints implemented:

- [Custom Bars (OHLC)](https://massive.com/docs/rest/stocks/aggregates/custom-bars)
- [Daily Market Summary (OHLC)](https://massive.com/docs/rest/stocks/aggregates/daily-market-summary)
- [Ticker Overview](https://massive.com/docs/rest/stocks/tickers/ticker-overview)
- [Float](https://massive.com/docs/rest/stocks/fundamentals/float)
- [News](https://massive.com/docs/rest/stocks/news)
- [Splits](https://massive.com/docs/rest/stocks/corporate-actions/splits)
# Momentum Dashboard

The local decision console exposes the scanner, opening-list candidates,
watchlist, open positions, and trade review through a Go API and Next.js UI.

```bash
# Apply all migrations, then start API + Redis + dashboard.
make migrate-up
make dashboard-up

# Browser
open http://localhost:3001
```

For local development, run `mip serve --addr :8080 --quote-tickers OPK` and
`make dashboard-dev` in separate terminals. The Webull QUOTE stream writes the
latest confirmed top-of-book plus replay history, while TICK writes transaction
history and feeds the execution order-flow window. The UI labels old quotes as
stale; it does not present cached quotes as live.
