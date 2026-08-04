# Session-Boundary Catalyst Study — 2026-07-30

สถานะ: retrospective research; ยังไม่ใช่ live-certified strategy  
Timezone: เวลาที่ใช้ในกฎเป็น US Eastern Time (ET)

## Thesis ที่ทดสอบ

ซื้อหุ้นที่มี catalyst ก่อน regular close ประมาณ 5–10 นาที แล้วถือผ่าน
after-hours open เวลา 16:00 ET เพื่อรับการ price discovery รอบใหม่ หากผิดทางให้
stop loss

สมมติฐานนี้ประกอบด้วยสองส่วนที่ต้องแยกกัน:

1. **Catalyst edge** — ข่าวมีผลต่อมูลค่าหรือความคาดหวังจริงและตลาดยัง price-in
   ไม่หมด
2. **Session-boundary effect** — closing auction, liquidity reset และผู้เล่น
   extended-hours ทำให้ราคาเกิด price discovery รอบใหม่หลัง 16:00 ET

Session boundary เพียงอย่างเดียวไม่ใช่เหตุผลให้ซื้อ

## หลักฐานระดับนาทีของตัวอย่างวันที่ 30

ราคา entry คือแท่งสุดท้ายในช่วง 15:45–15:55 ET และผลลัพธ์วัดจาก 16:00–20:00 ET
จาก Massive minute aggregates

| Ticker | Entry | AH high | MFE | MAE | AH end | First thresholds |
|---|---:|---:|---:|---:|---:|---|
| AXTI | 46.81 | 62.87 | +34.31% | -0.23% | +24.25% | +10% 16:05, +20% 16:27 |
| CYCU | 1.61 | 3.00 | +86.34% | -10.56% | +34.16% | +10/+20% 16:00, -5% 16:07, +50% 16:24 |
| KUST | 1.14 | 1.87 | +64.04% | -2.28% | +58.77% | +10% 16:33, +20% 16:39, +50% 17:01 |
| MGRX | 0.2888 | 0.95 | +228.95% | +2.46% | +141.73% | +10/+20% 16:01, +50% 16:05 |
| SBEV | 0.3399 | 0.695 | +104.47% | -1.97% | +66.61% | +10% 18:07, +20/+50% 18:17–18:18 |

ตัวอย่างทั้งห้าถูกเลือกหลังเห็นผลแล้ว จึงมี survivorship bias สูงและห้ามใช้
win rate 5/5 เป็นค่าคาดหวัง

## ตัวอย่างทั้งห้าเป็นคนละ setup

### AXTI — scheduled binary release

บริษัทประกาศล่วงหน้าว่าจะรายงานผล Q2 หลังตลาดปิดวันที่ 30 กรกฎาคม การรู้เวลา
ไม่เท่ากับรู้ทิศทางผลประกอบการ การซื้อก่อน 16:00 จึงเป็น pre-earnings binary
position ไม่ใช่การซื้อหลังเห็น positive catalyst

### MGRX — public catalyst, delayed price discovery

Form 8-K ถูกเผยแพร่ก่อนตลาดปิดและระบุ business combination กับ Nuclea โดย
ผู้ถือหุ้น Nuclea จะถือประมาณ 96% และผู้ถือหุ้น MGRX เดิมประมาณ 4% บน fully
diluted basis พร้อมเงื่อนไข PIPE ขั้นต่ำ $15M และ approvals หลายรายการ

รูปแบบนี้สนับสนุน lane `PUBLIC_CATALYST_UNDERREACTION`: ข่าวมีนัยสำคัญและ
เปิดเผยแล้ว แต่มีทั้ง dilution/closing risk จึงต้อง score คุณภาพและเงื่อนไข
ของดีล ไม่ใช่ดู headline อย่างเดียว

### KUST — ownership filing before close

Schedule 13D ถูก SEC รับเวลา 10:49:23 ET วันที่ 30 กรกฎาคมและระบุ Ryan Todd
Martin, 280,000 shares และ 100% of class ใน cover data; ข่าวถูกนำเสนอในภายหลัง
ก่อนตลาดปิด ราคาเพิ่มต่อหลัง 16:00 แต่ไม่ได้ spike ทันที: first +10% เกิด 16:33

นี่เป็น delayed-reaction setup ที่ต้องตรวจ consistency ของ filing ด้วย เพราะ
จำนวนหุ้นใน Item 5 และ cover data ไม่สอดคล้องกันทั้งหมด

### CYCU — continuation after an existing regular-session spike

ราคาปิด regular ที่ประมาณ +496% จาก previous close แล้ว การซื้อ 15:55 ไม่ใช่
pre-spike prediction แต่เป็น high-volatility continuation CYCU แตะ +10/+20%
หลังเปิด AH แล้วกลับต่ำกว่า entry มากกว่า 5% ก่อนขึ้นถึง +50% ดังนั้น fixed
-5% stop จะออกจาก eventual winner ได้

### SBEV — late tape move, not boundary jump

ข่าว licensing ที่เห็นในหน้าจอเป็นข่าวเก่ากว่าวันที่ 30 และการขึ้น +10% แรกเกิด
ประมาณ 18:07 ET หรือมากกว่าสองชั่วโมงหลัง AH เปิด จึงต้องอยู่ใน
`TAPE_ONLY_DELAYED` lane ไม่ใช่ pre-close catalyst lane

## Control: ซื้อทุกวันก่อน close ไม่ได้มี edge

สำหรับห้าตัวอย่าง ย้อนดู 1–30 กรกฎาคมรวม 94 ticker-days:

| Outcome after 16:00 ET | Frequency |
|---|---:|
| Hit +10% | 12 / 94 = 12.8% |
| Hit +20% | 8 / 94 = 8.5% |
| Hit +50% | 4 / 94 = 4.3% |
| Touched -5% | 20 / 94 = 21.3% |

Median AH-end return ของแต่ละ ticker อยู่ประมาณ -0.38% ถึง +0.90% การซื้อ
เพราะใกล้ 16:00 โดยไม่คัด catalyst จึงไม่มีหลักฐานว่าได้เปรียบ

ใน raw universe ที่ระบบ subscribe วันที่ 30 มี 68 symbols ที่มี tick ทั้งก่อน
และหลัง close:

- median MFE +1.47%
- hit +10% 19.12%
- hit +20% 7.35%
- hit +50% 2.94%
- touched -5% 32.35%

## Regular-session historical proxy

Daily market-wide bars วันที่ 15 มิถุนายน–29 กรกฎาคม, common stocks,
previous close $0.20–$20, volume อย่างน้อย 1M และ exclude split ที่ฐานข้อมูลรู้จัก:

- spike +50% จำนวน 331 symbol-days
- median open gap +29.88%
- 56.8% เปิด gap อย่างน้อย +20%
- 43.2% ยัง gap ต่ำกว่า +20% แล้วค่อยขยายอย่างน้อย +20% ระหว่าง regular

ผลนี้สนับสนุนการแยก `GAP_AND_GO` ออกจาก `INTRADAY_DISCOVERY` แต่เป็น daily
proxy ไม่ใช่หลักฐานระดับ tick และ split registry ยังไม่สมบูรณ์ จึงห้ามใช้ตัวเลข
นี้รับรอง live strategy

## News coverage finding

Massive News วันที่ 24–30 กรกฎาคมมี 989 records แต่จับคู่ได้เพียง 2 ข่าวกับ
รายชื่อ top-five regular movers ของแต่ละวันใน sample วันที่ 24–29 ขณะที่
MGRX, KUST และ CYCU ต้องพบจาก SEC/issuer/แหล่งอื่น

`NO_MASSIVE_NEWS` จึงต้องหมายถึง `UNKNOWN_CATALYST_COVERAGE` ไม่ใช่
`NO_CATALYST` และระบบยังไม่ควรเปิด live pre-position lane จนกว่า SEC, issuer
IR และ realtime headline ingestion จะครบและวัด `available_at` ได้

## Strategy lanes ที่ควร forward-test

### 1. PUBLIC_CATALYST_UNDERREACTION

อนุญาต shadow pre-position เวลา 15:50–15:57 ET เฉพาะเมื่อ:

- official source ยืนยันก่อน decision time
- catalyst เป็น earnings result, raised guidance, material contract,
  regulatory/clinical result, definitive merger หรือ ownership filing
- ไม่มี offering/dilution/delisting/reverse-split red flag ที่ยังไม่ได้หักคะแนน
- ตลาดยังไม่ขยายเกิน entry window
- spread, quote freshness, liquidity และ order flow ผ่าน

เริ่มด้วย 0.25R probe แล้ว scale เฉพาะเมื่อมี AH confirmation

### 2. SCHEDULED_RELEASE

ห้ามถือเพียงเพราะรู้ว่า earnings จะออก รอ parse ตัวเลขจริงและ guidance หลัง
release แล้วจึงใช้ event-time confirmation

### 3. EXTENDED_CONTINUATION

สำหรับหุ้นที่วิ่งไปแล้วมาก ให้ใช้ reclaim, higher low, volume re-acceleration
และ persistent book pressure ห้ามนับเป็น pre-spike prediction

### 4. TAPE_ONLY_DELAYED

เมื่อไม่พบ catalyst ที่ยืนยันได้ ให้เริ่มหลัง price/volume/order-flow trigger
เท่านั้น ห้าม pre-position

## Premarket และ regular

- Premarket ไม่ควรซื้อก่อน 04:00 ET เพราะบัญชีไม่มี overnight entitlement
  และระบบยืนยันราคา/สภาพคล่องก่อนเปิด premarket ไม่ได้ ให้เริ่มหลัง 04:00
  confirmation
- Regular session ใช้ event-time trigger เมื่อข่าวออก ไม่ใช้เวลา 09:30 เป็น
  signal โดยตัวมันเอง

## Execution constraints

Webull native STOP_LOSS ของ adapter ปัจจุบันรองรับ CORE session เท่านั้น
หลัง 16:00 ต้องใช้ virtual stop ที่เฝ้า tick แล้วส่ง marketable limit sell ซึ่ง
ไม่รับประกัน fill ที่ -5% ในหุ้น gap/บาง ดังนั้นทุก extended-hours probe ต้อง:

- จำกัด notional และ risk เล็กกว่าระบบ regular
- ตรวจ feed freshness ก่อนเข้าและตลอดเวลาที่ถือ
- มี slippage reserve และ max-spread kill
- lock fee-adjusted profit หลังแตะ profit floor
- หยุด entry ใหม่เมื่อ feed/research/quote ไม่สมบูรณ์

## Decision

รับ thesis เป็น `boundary-catalyst shadow experiment` แต่ไม่รับกฎ
“มีข่าวแล้วซื้อทุกตัวก่อน close” และยังไม่ promote เป็น live autonomous entry
จนกว่าจะมี out-of-sample cycles แยกตาม lane พร้อมผล net of fees/slippage

