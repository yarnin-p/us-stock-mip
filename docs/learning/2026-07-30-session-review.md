# Session Review — 2026-07-30 (US Eastern)

สถานะ: ปิดชุดข้อมูล intraday หลัง after-hours เวลา 20:00 ET  
จุดประสงค์: เก็บหลักฐานแบบ point-in-time สำหรับ replay, backtest,
forward-test และการสร้าง label ของ spike/entry/exit โดยไม่ใช้ข้อมูลอนาคต

## ข้อมูลที่เก็บแล้ว

ข้อมูลดิบอยู่ใน PostgreSQL volume ของระบบ ไม่ได้สรุปทับข้อมูลต้นฉบับ:

- `market_quote_history`: top-of-book quote 477,831 events จาก 872 symbols
- `market_trade_ticks`: trade ticks 1,547,091 events จาก 808 symbols
- `scanner_signals`: 61,746 observations จาก 1,311 symbols
- `execution_orders`: 244 order records
- `execution_fills`: 45 fill records
- `strategy_cycle_outcomes`: 22 closed shadow cycles

ช่วงเวลาที่เก็บได้คือ 05:18:00–20:00:00 ET ข้อมูลช่วง
04:00–05:18 ET ไม่มี เพราะ collector ยังไม่ออนไลน์ ส่วน overnight ไม่มีตาม
data entitlement ที่ผู้ใช้ตั้งใจไม่สมัคร ข้อมูลนี้จึงต้องถูกทำเครื่องหมายว่า
`left-censored` ห้ามตีความว่าเป็นการตรวจพบก่อน spike

Source coverage:

| Stream | Source | Events | Symbols |
|---|---:|---:|---:|
| Quote | `webull_nasdaq` | 477,244 | 871 |
| Quote | `legacy_webull_unknown` | 587 | 24 |
| Tick | `webull_nasdaq` | 1,515,695 | 800 |
| Tick | `legacy_webull_unknown` | 31,396 | 72 |

## ผล shadow execution

| Metric | Result |
|---|---:|
| Closed cycles | 22 |
| Win / non-win | 10 / 12 |
| Net PnL | -$11.91 |
| Average realized return | -0.736% |
| Average MFE | +4.944% |
| Average MAE | -3.014% |

ผลแยกตาม ticker:

| Ticker | Cycles | Wins | Net PnL | Max MFE | Worst MAE |
|---|---:|---:|---:|---:|---:|
| DGNX | 1 | 0 | -$4.35 | 0.000% | -4.469% |
| NUWE | 14 | 5 | -$4.40 | +31.323% | -8.914% |
| STKH | 5 | 4 | -$0.06 | +8.840% | -3.464% |
| XRX | 2 | 1 | -$3.10 | +4.587% | -3.930% |

หลักฐานสำคัญคือ MFE เฉลี่ยเป็นบวก +4.944% แต่ผลจริงเฉลี่ยติดลบ
-0.736% แปลว่าปัญหาหลักของ session นี้ไม่ใช่ “หา ticker ที่วิ่งไม่ได้”
เพียงอย่างเดียว แต่เป็น profit capture, re-entry churn และ entry หลัง expansion
ด้วย

ตัวอย่าง profit giveback:

| Ticker | Entry | Exit | Realized | MFE | Giveback |
|---|---:|---:|---:|---:|---:|
| NUWE | 4.31 | 5.2453 | +21.550% | +31.323% | 9.772% |
| STKH | 4.27 | 4.30 | +0.551% | +7.541% | 6.990% |
| NUWE | 5.01 | 5.00 | -0.328% | +6.587% | 6.915% |
| NUWE | 4.17 | 4.15 | -0.635% | +5.516% | 6.150% |
| NUWE | 5.19 | 5.24 | +0.840% | +6.744% | 5.904% |

## Spike timing ที่ควรใช้สร้าง label

เวลาทั้งหมดเป็น ET และมาจาก scanner observations:

| Ticker | First +10% | First +30% | First +50% | First +100% | Max move |
|---|---:|---:|---:|---:|---:|
| CYCU | 08:20:33 | 08:20:33 | 08:20:47 | 11:20:25 | +558.77% |
| NUWE | 05:17:57 | 05:17:57 | 05:17:57 | 05:20:44 | +227.51% |
| MGRX | 07:00:14 | 07:00:14 | 07:00:14 | 17:15:24 | +220.13% |
| AMZE | 05:29:15 | 17:47:59 | 17:53:29 | — | +99.80% |
| DFNS | 05:17:57 | 05:17:57 | 05:17:57 | — | +99.12% |
| STKH | 05:17:57 | 05:17:57 | 05:17:57 | — | +80.96% |
| KUST | 12:42:46 | 16:49:40 | 17:05:24 | — | +57.07% |
| KSCP | 09:52:47 | 12:51:00 | 15:26:53 | — | +51.43% |

## Patterns ที่ต้องเรียนรู้

1. **Early mover persistence / stair-step** — KUST, KSCP และ AMZE อยู่ที่ประมาณ
   +10% หลายชั่วโมงก่อนขยายเป็น +50% จึงต้องเรียนรู้
   `future_max_return` และ `time_to_threshold` ที่ 15m/30m/1h/session
   ไม่ใช่รอให้เป็น top gainer แล้วจึง flag
2. **Late first sight** — NUWE, STKH, CYCU และ DFNS ขึ้นเกิน +30–50%
   ตั้งแต่ observation แรก การพบชื่อเหล่านี้ไม่ใช่ pre-spike prediction แต่เป็น
   continuation setup และต้องติด label `left_censored/late_at_first_detection`
3. **Second leg หลัง long consolidation** — MGRX ถูกพบที่ +50% ตั้งแต่ 07:00
   แต่ไปถึง +100% หลัง 17:15 รูปแบบนี้ต้องดู volume re-acceleration,
   reclaim, higher low และ book replenishment ไม่ใช่ไล่ซื้อจาก percentage gain
4. **Catalyst lane และ tape-only lane เป็นคนละแบบ** — ข่าว/filing ที่ยืนยันได้
   ควรเพิ่ม thesis persistence แต่ low-float/order-flow runner อาจเกิดโดยไม่มี
   catalyst ที่ feed ยืนยัน ต้องแยก model lane ไม่ใช้ค่า news เดียวตัดสินทุกตัว
5. **Entry exhaustion** — ระยะจาก VWAP/session high, move ที่เกิดไปแล้ว,
   pullback depth, จำนวน re-entry และ buy-pressure decay ต้องเป็น feature หลัก
   เพื่อไม่ซื้อท้าย expansion
6. **Profit capture** — สร้าง label MFE, MAE, realized capture ratio,
   max giveback และ `reached_profit_floor_but_closed_nonprofit`
   เพื่อฝึก exit policy แยกจาก entry model
7. **Re-entry churn** — NUWE มี 14 cycles จึงต้องมี re-entry budget/cooldown
   ที่ reset ได้เมื่อมีหลักฐานใหม่ เช่น new high, catalyst ใหม่ หรือ volume regime
   ใหม่ ไม่ใช่ reset ด้วยเวลาเพียงอย่างเดียว
8. **Book persistence ไม่ใช่ snapshot เดียว** — เรียนรู้ bid persistence,
   replenishment, cancel/replace rate และ trade-through หลาย observations
   เพราะ bid ขนาดใหญ่สามารถหายได้
9. **Corporate-action normalization** — reverse split เช่น DFNS ต้องถูกปรับราคา
   และ exclude จาก spike label ที่เกิดจาก corporate action
10. **Missing news ไม่เท่ากับ no catalyst** — วันที่ 30 มีข่าวที่เก็บได้เพียง
    2 records และเป็น REPL ทั้งหมด; key runners NUWE/MGRX/CYCU/KUST/AMZE/STKH/KSCP
    ไม่มีข่าวในฐานข้อมูล จึงต้องใช้ state `unknown/missing` และทำ news backfill
    ก่อนนำ catalyst feature ไป train

## ข้อมูลที่ยังไม่รวม

- Daily bars, EOD feature snapshots และ daily rankings จะถูก finalize โดย scheduled
  EOD jobs หลัง session; intraday raw events ข้างต้นเก็บครบตาม coverage window แล้ว
- Manual trades ใน Dime เป็นคนละ broker account และข้อมูล fill/quantity บางรายการ
  ไม่ครบ จึงไม่ถูกเขียนปลอมเป็น Webull execution fills ต้องเพิ่ม Dime import หรือ
  manual fill ledger ที่ระบุ broker ก่อนจึงนำมาคำนวณ PnL/training อย่างถูกต้อง
- ข่าวสำคัญของ key runners ยังขาด ต้อง backfill จาก provider ที่มี entitlement
  และเก็บทั้ง `published_at` กับ `available_at` เพื่อป้องกัน look-ahead bias
