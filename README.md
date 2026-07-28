# Momentum Intelligence Platform (MIP)

Sprint 1 ของ AI Quant Research Platform สำหรับ US Small Cap Momentum: วาง data foundation ด้วย Go, PostgreSQL, Massive REST API และ ETL ที่รันซ้ำได้โดยไม่สร้างข้อมูลซ้ำ

## สิ่งที่ทำแล้ว

- Go repository แบบ right-sized: `cmd/mip` เป็น composition root และ business code อยู่ใน `internal/`
- PostgreSQL migrations สำหรับ `stocks`, `daily_prices`, `intraday_prices`, `news`, `sec_filings`, `trades`
- Massive adapter สำหรับ ticker overview, free float และ historical aggregate bars
- ETL สำหรับ daily/minute OHLCV พร้อม upsert
- Docker Compose สำหรับ PostgreSQL และ migration runner
- Unit tests ของ Massive client, pagination/security checks, config และ ETL; PostgreSQL integration test แยกด้วย build tag

## เริ่มใช้งาน

ต้องมี Go 1.26+, Docker และ Docker Compose

```bash
cp .env.example .env
# เติม MASSIVE_API_KEY และเปลี่ยน POSTGRES_PASSWORD ใน .env

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
| `MASSIVE_API_KEY` | yes | - | Massive REST authentication |
| `MASSIVE_BASE_URL` | no | `https://api.massive.com` | Override for tests/environment |
| `HTTP_TIMEOUT` | no | `15s` | Per-request timeout |
| `DB_MAX_CONNS` | no | `10` | Maximum pool connections |
| `DB_MIN_CONNS` | no | `2` | Minimum pool connections |
| `LOG_LEVEL` | no | `info` | `debug`, `info`, `warn`, `error` |

Webull credentials are intentionally reserved for Sprint 6 and are not read by Sprint 1 code.

## Data flow

```text
CLI job -> ETL runner -> Massive adapter
                    -> PostgreSQL store -> normalized tables
```

The Massive aggregate adapter follows `next_url` pagination only when it remains on the configured Massive origin, never includes the API key in errors, and caps a request at 100,000 bars. Data writes use parameterized SQL, bounded batches, atomic transactions, and conflict-safe upserts.

Massive endpoints implemented:

- [Custom Bars (OHLC)](https://massive.com/docs/rest/stocks/aggregates/custom-bars)
- [Ticker Overview](https://massive.com/docs/rest/stocks/tickers/ticker-overview)
- [Float](https://massive.com/docs/rest/stocks/fundamentals/float)
