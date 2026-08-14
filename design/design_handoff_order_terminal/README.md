# Handoff: Order Terminal + Portal Hub (TradeEdge / us-stock-mip)

## Overview

A redesign of the pre-submission **order terminal** (bracket ticket) and a new **portal hub** that acts as the launcher for the product's sub-apps. The terminal lets a trader size a position, set stop and target, configure a four-rung exit ladder, review the whole bracket, and send it to the engine. The hub is the entry point: today's state, a quick "start a new bracket" action, and cards into the five apps.

Target repo: `yarnin-p/us-stock-mip` (branch `main`), web front end in `web/` — Next 16 + React 19, plain CSS classes in `web/app/globals.css`. The existing ticket lives in `web/app/TicketPanel.tsx` (route `web/app/ticket/page.tsx`) and talks to `POST /ticket/preview` and `POST /ticket/submit`.

## About the Design Files

The files in this bundle are **design references written in HTML** (Design Components: a single `.dc.html` file each, rendered by `support.js`). They are prototypes of the intended look and behaviour — **not production code to copy**. The task is to **recreate them inside the existing `web/` app** using its established patterns: React function components, the plain-CSS class approach already used in `globals.css` (`tk-*`), and the real API contract. Do not port the inline styles or the DC runtime.

Open any `.dc.html` in a browser to interact with it (all state is local, no network).

## Fidelity

**High fidelity.** Colors, type sizes, weights, radii, spacing, motion timings and copy are final and should be matched. Data values are representative fixtures — wire them to the real preview response.

## Screens / Views

### 1. Portal Hub — `Portal Hub.dc.html`

**Purpose:** launcher. Answer "what happened while I was away" and "where am I going", then get out of the way.

**Layout** (page padding 14px, 14px gaps, column flex):
1. **Top bar** — pill group: brand pill (`TradeEdge portal`, amber 28px rounded-9px monogram) · right side: market-status pill with pulsing amber dot, `PAPER` pill (amber fill, ink text, letter-spacing .1em), 40px circular avatar.
2. **Hero row** — CSS grid `minmax(0,1.15fr) minmax(0,1fr)`, 14px gap:
   - **Left plane** `#1c1e21`, radius 30, padding 30/32: greeting 13px `#9aa0a6`; H1 "Where to today?" 52px/700, letter-spacing −.055em, line-height .95; bottom strip of 4 stat cards (`repeat(auto-fit, minmax(148px,1fr))`, 10px gap) each `#232629` radius 20, padding 16/18 — label 11.5px `#9aa0a6`, value `clamp(19px,2.1vw,26px)`/700 ls −.04em, sub 11.5px. Stats: Portfolio equity `$20,000` / paper account; Risk used today `$20.47` amber / 10% of budget; Brackets live `4` / engine maintained; Open P/L `+$412.60` green / across live plans.
   - **Right plane** amber `#f0a52e`, ink `#17181a`, radius 30: heading `START A NEW BRACKET` 14px/800; label `Ticker` 15px/600 opacity .8; **ticker input** 46px/700 ls −.05em, uppercase, transparent bg, `border-bottom: 3px dashed rgba(23,24,26,.34)` → hover `rgba(23,24,26,.7)` → focus solid `#17181a`; `::placeholder` `rgba(23,24,26,.62)`/700; note box `rgba(23,24,26,.14)` radius 16, 12.5px/600: "Nothing is stored before you submit — the terminal opens empty every time, and a bracket only exists once the engine accepts it."; CTA pill `#17181a` / amber text, padding 15/30, radius 999 → hover `#000`, active `scale(.96)`; hint "or press T to jump to this field" 12.5px/700 opacity .78.
3. **All apps** — heading 20px/700 + sub 13px `#9aa0a6`; grid `repeat(auto-fit, minmax(292px,1fr))`, 12px gap. Each card: radius 28, padding 24/26/22, min-height 232, flex column, hover `translateY(-4px)` + `0 22px 50px -30px rgba(0,0,0,.9)`, 180ms `cubic-bezier(.22,1,.36,1)`. Contents: 42px rounded-14 mark tile holding one geometric shape (dot / ring / bar / tall bar / diamond via `rotate(45deg)`), category tag 12px/700 ls .08em, name 24px/700 ls −.035em, description 13px, footer row with state text 13px/700 and 32px circular `›` button.
   - Cards & skins: **Order Terminal** amber fill, ink text, tag `PLAN`, state "opens empty", links to the terminal · **Scanner** dark `#1c1e21`, `FIND`, "34 hits today" · **Positions & Orders** green `#4fc38a`, ink, `MANAGE`, "4 maintained · 214 total" · **Performance** dark, `REVIEW`, "updated 06:10" · **Risk & Controls** red `#f2705c`, ink, `GUARD`, "gate armed".
   - Text opacity per skin (contrast-tuned, keep these): coloured cards tag .85–.88 / desc .92–.94; dark cards tag .68 / desc .74.
4. **Today in the portal** — `#1c1e21` plane; rows `#232629` radius 18, grid `72px 150px 1fr 128px`, hover `#2b2f33`: time, app name with colour dot, event text, right-aligned value.

### 2. Order Terminal — `Terminal-H-PopBlocks.dc.html`

**Purpose:** write and submit one bracket. Two steps: the ticket (size + stops) and the exit ladder (four rungs), then review → send.

**Layout** (page padding 14px, column, 14px gaps):
1. **Portal bar** — `‹ All apps` pill (links to hub), breadcrumb pill "TradeEdge portal / Order Terminal", right: ticker pill, `PAPER` amber pill, avatar.
2. **Title row** — market status line with pulsing dot; H1 "Order Terminal" 56px/700 ls −.055em; right: quick R:R pills `1.5 : 1`, `2 : 1`, `3 : 1` (sets TP from SL × ratio).
3. **Main grid** `minmax(0,1fr) minmax(0,1fr)`:
   - **Ticket plane** `#1c1e21` radius 30 padding 26:
     - **Side tabs** — 2-up grid, each radius 20 padding 18, 19px/800 centred. Active BUY = green `#4fc38a` on ink; active SELL = red `#f2705c` on ink; inactive `#232629` with `#9aa0a6` text. Active press `scale(.98)`.
     - **Ticker / Entry** — two `#232629` radius-20 field cards; inputs 26px/700 with dashed underline affordance (`2px dashed #3a4045` → hover `#6f757a` → focus `2px solid #f0a52e`), caret amber.
     - **Position size** — `#232629` card: label + segmented pill (`By cash` / `By risk`, active amber on ink); amount input 30px/700 with `$` prefix and live "= N shares"; quick-amount pills `$100/$200/$500/$1000` (hover amber).
     - **SL / TP blocks** — 2-up, min-height 208, radius 26, **solid fills**: SL `#f2705c`, TP `#4fc38a`, all text ink `#17181a`. Header row: label 15px/800 + two 30px circular steppers `−` / `+` (`rgba(23,24,26,.14)`, hover ink fill with the block colour as text, active `scale(.88)`; step 0.5 in percent mode, 0.01 in price mode). Value input 64px/700 ls −.055em with 3px dashed ink underline (hover .7 alpha, focus solid + `rgba(255,255,255,.24)` wash). Footer 15px/600: `$2.07 · −฿688`. Hover lifts the block `translateY(-3px)` with a coloured shadow.
     - **Measure row** — `percent` / `price` segmented pill; Equity and Fee % compact pill fields (right-aligned inputs).
   - **Risk column** (sticky, top 14):
     - **Risk plane** amber `#f0a52e`, ink text, radius 30: `WHAT YOU ARE RISKING` 14px/800; **฿ risk 76px/700** ls −.06em with secondary line `$20.47 · USD/THB 33.60` 17px/700 opacity .72; right-aligned `0.10%` 34px/700 + "of equity"; 12px budget meter (`rgba(23,24,26,.18)` track, ink fill, width = risk ÷ 1% of equity, 160ms transition) with caption "risk budget used · 1% of equity" and the used-% in 800. The whole plane runs a 420ms brightness `flash` whenever the risk number changes.
     - **Price stack** — three radius-22 solid rows, ink text: TP green, Entry `#eceded`, SL red; label 14px/800 left, `+25.2%` / `86 sh · $197.80` / `−10.0%` 13px/600 opacity .72, price 28px/700 ls −.04em.
     - **Stats plane** `#1c1e21` radius 26, 3-up: reward:risk, break-even win rate, gain at target (green). Values 30px/700, labels 11.5px `#9aa0a6`.
4. **Exit ladder plane** — heading + sub; preset pill group `conservative / balanced / runner` (active amber on ink); cards `repeat(auto-fit, minmax(268px,1fr))`. Card: `#232629` when armed / `#1e2023` when off, radius 24 padding 20, hover `translateY(-2px)`; 34px circular rung number (armed amber on ink, off `#2c3034`/`#9aa0a6`, press `scale(.9)`); name 16.5px/700; description 12.5px `#8f959a`; field rows `#191b1e` radius 14 with 62px right-aligned inputs (focus `inset 0 0 0 2px #f0a52e`); note 11.5px `#9aa0a6`.
   - Rung content: **1 Break-even** (Arm at gain %, Lift SL to gain %) · **2 Profit lock** (same two fields) · **3 Trail** (SL trails from gain %, TP widens from gain %, SL below high %, TP above high %) · **4 Partial take-profit** (Arm at gain %, Sell % of position, Skip under N shares). Preset values: conservative `[2,1] [4,2.5] [8,16,7,12] [20,40,10]`; balanced `[3,1.5] [6,3] [10,20,10,15] [30,25,10]`; runner `[4,1] [10,4] [14,28,16,22] [45,20,10]`.
5. **Plans plane** — title switches with tabs `In play N` / `Closed`; hint copy explains that only engine-maintained brackets are here and the full history lives in the Orders app; `Open Orders app ›` pill; header row 11.5px `#9aa0a6`; rows `#232629` radius 16 in a 264px-max scroll area; footer line + `See the full history ›`.
6. **Review sheet** (only while confirming) — sticky `bottom: 88px`, radius 30, fill = side colour (green for BUY, red for SELL), ink text: `REVIEW — PAPER, simulated` 12.5px/800; `BUY 86 KWM @ $2.30` 34px/700; `SL … · TP … · risk ฿688 ($20.47) · 4 of 4 rungs armed` 15px/600; right: `Cancel` (ink 14% wash) and `SEND BUY` (ink fill, side-colour text, 800).
7. **Success sheet** (after send) — same slot, green fill: "Bracket accepted · order #10428", the engine-maintains line, and `Open in Positions ›`.
8. **Sticky action bar** — `bottom: 14`, radius 999, fill `#eceded`, ink text, padding 12/12/12/28: four live stats (Position, Risk in red `#b7381f`, Reward : risk, Exit ladder "N of 4 rungs"), then the keyboard hint 11.5px/600 opacity .62 and the primary pill `#17181a` / amber reading `BUY 86 KWM`.

## Interactions & Behavior

- **Live pricing.** Every field change recomputes shares, cost, risk, reward, R:R, break-even win rate, budget meter. In the real app this is the debounced `POST /ticket/preview` call that already exists in `TicketPanel.tsx` (180ms, abortable, request-ID guarded) — keep that logic.
- **Side tabs** switch BUY/SELL and reset any pending review.
- **Steppers** on SL/TP: ±0.5 in percent mode, ±0.01 in price mode, clamped at 0. The repo's price-aware `step()` (0.1 / 0.01 / 0.001 by magnitude) is the better rule — keep the repo's version when wiring.
- **Unit toggle** percent ↔ price converts the current values so the numbers never jump.
- **R:R pills** set TP % = SL % × ratio and force percent mode.
- **Keyboard**: `Enter` in any input opens the review; `Enter` again sends; `Esc` cancels the review. (Matches the existing panel.)
- **Review → send** is two-step by design: nothing reaches the broker until `SEND`. Success replaces the sheet with the accepted state; the design shows a paper-mode label — a live mode must be visually unmistakable (repo already distinguishes `tk-live` / `tk-paper`; carry that through: live should not be able to look like paper).
- **Exit ladder** rungs toggle on/off by clicking the number; presets overwrite all four rungs; disabled rungs fade (name `#9aa0a6`, card `#1e2023`) but keep their values.
- **Plans tabs** filter live (Armed/Trailing) vs closed (last 5). Pagination deliberately does **not** exist here — the full, filterable history belongs to the Orders app.
- **Hub**: `T` focuses the ticker field (ignored while typing elsewhere); the typed ticker is passed as `?ticker=XXX` and the terminal reads it on load.

**Motion** (all short, one loop only): entrance `popIn` (opacity + 10px rise) 460–500ms `cubic-bezier(.22,1,.36,1)` staggered 0/90/170/240ms · hover lifts 180ms · button press `scale(.88–.98)` 110–120ms · risk-plane `flash` brightness 1→1.16→1 420ms on value change · budget meter width 160ms · market dot `pulse` 2.4s infinite · review sheet 220ms.

## State Management

Local: `side` (BUY|SELL), `ticker`, `entry`, `amount`, `sizeMode` (money|risk), `unit` (pct|price), `slPct`/`tpPct` and `slAbs`/`tpAbs`, `portfolio`, `fee`, `rate` (USD/THB, 33.6 fixture — comes from `usd_thb` in the preview response), `preset`, `stages[4] {on, values[]}`, `confirming`, `sent`, `planTab`.

Derived (server-side in the real app): `shares = floor(amount / entry)`, `cost = shares × entry`, `feeCost = cost × fee%`, `risk = shares × (entry − sl) + feeCost`, `reward = shares × (tp − entry) − feeCost`, `rr = reward / risk`, `breakEvenWinRate = risk / (risk + reward)`, `budgetUse = risk / (equity × 1%)`, `riskThb = risk × rate`.

Data to fetch: preview (`/ticket/preview`), submit (`/ticket/submit` with `send: true`), live/maintained brackets for the plans list, USD/THB rate, and the hub's today-stats + activity feed.

## Design Tokens

Colours — page `#151618`; planes `#1c1e21`; raised `#232629`; deeper `#191b1e`; hover `#2b2f33` / `#262a2e`; field border `#3a4045`, hover `#6f757a`; ink `#17181a`; paper `#eceded`; text dim `#b6bbbf`, label `#8f959a`, small-text dim `#9aa0a6` (do **not** go dimmer than this at ≤12px — AA), disabled `#2c3034`; amber `#f0a52e`; green `#4fc38a`; red `#f2705c`; risk-on-white `#b7381f`.

Type — Plus Jakarta Sans 400/500/600/700/800, `font-variant-numeric: tabular-nums` globally. Scale: 84/76/64/56/52/46/34/30/28/26/24/21/20/19/17/16.5/15/14.5/14/13.5/13/12.5/12/11.5/11. Negative tracking on big numbers (−.03 to −.06em).

Radii — 999 (pills) / 30 (planes) / 28 / 26 / 24 / 22 / 20 / 18 / 16 / 14 / 12 / 10.
Spacing — 4 / 6 / 8 / 10 / 12 / 14 / 16 / 18 / 20 / 22 / 24 / 26 / 28 / 30 / 32.
Shadows — hover only: `0 18px 40px -24px <colour>` on the SL/TP blocks, `0 22px 50px -30px rgba(0,0,0,.9)` on hub cards.

## Assets

None. Every mark is a CSS shape (circle, ring, square, rotated square, bar) — no icon set, no images. Fonts load from Google Fonts (`Plus+Jakarta+Sans`); self-host in production.

## Files

- `Terminal-H-PopBlocks.dc.html` — **the terminal to build** (final direction).
- `Portal Hub.dc.html` — **the hub to build**.
- `Terminal-G-Themes.dc.html` — theme exploration, 10 colour schemes over the same layout (`graphite` is the chosen one). Reference only; useful if theming is wanted later.
- `support.js` — the Design Component runtime that renders the `.dc.html` files locally. Not part of the deliverable.
- `github.md` — repo association, screen map, and the API/field notes gathered from `web/app/TicketPanel.tsx`.

## Known gaps vs the current repo

- The repo sizes by **shares** with ±100 steppers; this design sizes by **cash** (with a `By risk` mode stubbed). Decide which is canonical — the design's "= N shares" readout assumes cash.
- The repo's ticket has no exit-ladder concept yet; the four rungs need engine support (`arm at`, `lift to`, `trail from high`, `partial sell %`).
- The design's plans list, hub stats and activity feed are fixtures; endpoints do not exist yet.
- No draft persistence anywhere, by design — copy in both screens states this explicitly.
