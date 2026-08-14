# Mapping — Order Terminal redesign → existing code

Handoff: `design/design_handoff_order_terminal/` (Terminal-H-PopBlocks, graphite + amber).
Written 2026-08-14. Repo state: branch `feat/premarket-surge-hunter` @ `4f24cee`.

## The one decision that shapes everything

The handoff maps the terminal onto `web/app/TicketPanel.tsx` (route `/ticket`, API
`/ticket/preview` + `/ticket/submit`). That is the **older, simpler** order path. It has a
side toggle and a confirm step, and nothing else — no bracket, no ladder, no engine.

The capability the design actually describes lives on the **other** path:

| | `/ticket` path | `/execution/orders` path |
|---|---|---|
| Component | `TicketPanel.tsx` (11.6 KB) | `Terminal.tsx` (58.6 KB) |
| Route | `/ticket` | `/terminal` |
| API | `POST /ticket/preview`, `POST /ticket/submit` | `POST /execution/orders`, `…/{id}/preview`, `…/{id}/approve`, `…/{id}/submit`, `POST /brackets/{id}/arm` |
| Exit ladder | none | all four rungs, engine-backed |
| Bracket engine | no | yes |

**Decision: build the redesign on `Terminal.tsx` / `/terminal`.** The design's four rungs
already exist there. Rebuilding on `/ticket` would mean re-implementing the ladder and the
engine wiring that is already done and tested.

The designer synced from `main` on 2026-08-14T05:02Z. The 22 commits carrying the bracket
engine were on `feat/premarket-surge-hunter` and unpushed at that moment, which is why the
handoff's "Known gaps" says the ladder does not exist. It does.

## Exit ladder — 1:1, nothing to invent

This is the good news. The design's four rungs map exactly onto triggers that already run.

| Design rung | Design fields | Repo trigger (`internal/bracket/bracket.go`) | Repo state (`Terminal.tsx`) |
|---|---|---|---|
| 1 Break-even | Arm at gain %, Lift SL to gain % | `TriggerBreakEven` | `breakEvenOn`, `breakEvenAfter`, `breakEvenFloor` |
| 2 Profit lock | Arm at gain %, Lift SL to gain % | `TriggerProfitLock` | `profitLockOn`, `profitLockAfter`, `profitLockFloor` |
| 3 Trail | SL trails from %, TP widens from %, SL below high %, TP above high % | `TriggerTrailStop` + `TriggerTrailTarget` | `trailStopAfter`, `trailStopDistance`, `trailTargetAfter`, `trailTargetDistance` |
| 4 Partial take-profit | Arm at gain %, Sell % of position, Skip under N shares | `TriggerPartialTP` | `partialOn`, `partialAfter`, `partialFraction`, `partialMinShares` |

Also present and not in the design, because they are reconciliation rather than plan:
`TriggerFilled`, `TriggerStopFired`, `TriggerStopHandover`, `TriggerInitial`, `TriggerManual`.

Backed by migrations 49 (trigger values), 50 (stop-fired trigger), 51 (stop generation).
`internal/bracket/trigger_schema_test.go` parses the constants and asserts they match the
CHECK constraint, so adding a rung without a migration fails the test rather than the engine.

**To build:** the three presets (`conservative` / `balanced` / `runner`) do not exist. They
are pure UI — a preset writes the twelve numbers into existing state. Values from the handoff:

```
conservative  [2, 1]  [4, 2.5]   [8, 16, 7, 12]   [20, 40, 10]
balanced      [3, 1.5] [6, 3]    [10, 20, 10, 15] [30, 25, 10]
runner        [4, 1]  [10, 4]    [14, 28, 16, 22] [45, 20, 10]
```

## State

| Design | `Terminal.tsx` | Status |
|---|---|---|
| `ticker` | `ticker` | ✅ |
| `entry` | `entry` | ✅ |
| `amount` | `amount` | ✅ |
| `sizeMode` (money \| risk) | `basis` | ✅ same idea |
| `unit` (pct \| price) | `exitUnit` | ✅ |
| `slPct` / `slAbs` | `stopPct` | ⚠️ repo keeps percent only; add the absolute twin so the unit toggle converts instead of jumping |
| `tpPct` / `tpAbs` | `targetPct` | ⚠️ same |
| `portfolio` | `equity` | ✅ |
| `fee` | `feeRoundTrip` | 🔴 flat % — wrong for Dime, see below |
| `rate` (USD/THB) | — | ❌ add; comes from `usd_thb` in the preview response |
| `stages[4] {on, values[]}` | 14 flat fields | ✅ same data, reshape to an array |
| `preset` | — | ❌ build (UI only) |
| `side` (BUY \| SELL) | — | ❌ not in `Terminal.tsx`; exists in `TicketPanel.tsx`, port it |
| `confirming` | — | ⚠️ repo has a 4-step approve flow instead, see below |
| `sent` | `order`, `said` | ~ reshape |
| `planTab` | `mode`, `rows` | ~ reshape |
| — | `advanced`, `openId`, `manageId`, `tick`, `note` | ➕ repo-only, keep |

## Submit flow — 2 steps on screen, 4 on the wire

The design shows review → send. The real path is longer, and the extra steps are the audit
trail, so they stay:

```
design                 real calls
──────────────────────────────────────────────────────────
(edit fields)          POST /execution/orders            → create
                       POST /execution/orders/{id}/preview
[ Review sheet ]       POST /execution/orders/{id}/approve
[ SEND ]               POST /execution/orders/{id}/submit
                       POST /brackets/{id}/arm           → engine takes over
```

`approve` fires when the review sheet opens, `submit` + `arm` when SEND is pressed. The user
sees the two steps the design specifies. Nothing reaches the venue before SEND.

Keep from the design: `Enter` opens review, `Enter` again sends, `Esc` cancels. Keep from the
repo: the 180 ms debounced, abortable, request-ID-guarded preview.

## Fee model — the design is numerically wrong here

The design has one `Fee %` field. Dime charges:

```
commission = max($0.01 × shares, 0.15% × value)   then + 7% VAT
crossover at $6.67 / share
```

Below $6.67 the per-share floor binds and the effective rate climbs as price falls:

| price | effective fee per side |
|---|---|
| $19.93 | 0.16 % |
| $1.62 | 0.66 % |
| $0.42 | 2.55 % |

A constant percentage misprices risk on every trade under $6.67 — which is most of this
book. **Replace the `Fee %` field with a computed fee derived from price and share count**,
and show the resulting round-trip percentage as a read-only figure next to it. Keep an
override for other brokers.

## Liquidity — absent from the design, added here

Every number in the risk column answers *how much am I risking*. Nothing answers *can I get
out*. Two positions on 2026-08-13 made the case: AMS showed a 8.61 % spread with 5 shares
($8) on the bid, and FGI at $19.20 had 100 shares on the bid.

The engine already computes this — `ExitDepth`, `DepthRisk`, `SizeByShares` in
`internal/bracket/bracket.go`, with flags `DEPTH_CAPPED` / `DEPTH_THIN` / `DEPTH_UNKNOWN`,
and `Store.ExitDepth` reads the best bid from `market_quotes`. There is no surface for it.

**Add a plane under the risk plane, in the design's own language** (solid fill, ink text,
same radius and type scale): spread %, bid depth in dollars, max shares by the depth rule
(bid × 3), and one combined round-trip cost = spread + fee. When `DEPTH_THIN` or
`DEPTH_UNKNOWN` is raised, the plane turns red `#f2705c` and the size input shows the cap.

This also makes sizing three-mode, not two: cash, risk, and depth-capped.

## Theme

Graphite + amber, exactly as specified. Tokens go into `web/app/terminal.css` as custom
properties; the existing `tm-*` class prefix (155 uses in `Terminal.tsx`) stays.

```
page #151618 · plane #1c1e21 · raised #232629 · deeper #191b1e
hover #2b2f33 / #262a2e · field #3a4045 → #6f757a
ink #17181a · paper #eceded
dim #b6bbbf · label #8f959a · small #9aa0a6 (floor at ≤12px, AA) · disabled #2c3034
amber #f0a52e · green #4fc38a · red #f2705c · risk-on-white #b7381f
```

Type: Plus Jakarta Sans 400–800, `tabular-nums` globally, negative tracking (−.03 to −.06em)
on the large figures. Radii 999/30/28/26/24/22/20/18/16/14/12/10. Motion: `popIn` 460–500 ms
`cubic-bezier(.22,1,.36,1)` staggered 0/90/170/240, hover lift 180 ms, press scale .88–.98
110–120 ms, risk `flash` 420 ms, meter width 160 ms, market dot pulse 2.4 s, review sheet
220 ms. Fonts self-hosted, not Google Fonts.

`Terminal-G-Themes.dc.html` holds ten schemes over the same layout; `graphite` is chosen.
Structuring the tokens as custom properties keeps the others reachable later at no cost.

## Live vs paper

The handoff is right that live must never be able to look like paper. The repo already
separates `tk-live` / `tk-paper` and `Terminal.tsx` has `ModeBadge`. Carry it through: the
paper pill is amber-on-ink; live must differ in fill, not only in label.

## Work order

1. Tokens + type + motion into `terminal.css`; `Terminal.tsx` re-skinned to the new shape.
2. `side` (BUY/SELL) ported from `TicketPanel.tsx`.
3. `slAbs` / `tpAbs` twins so the percent ↔ price toggle converts.
4. Fee model: price-aware Dime calculation replacing the flat field.
5. Ladder presets (UI only — state already exists).
6. Liquidity plane wired to `ExitDepth` / `DepthRisk`.
7. Review sheet, success sheet, sticky action bar.
8. Plans plane fed by the existing bracket list.

## Not started — Portal Hub

`web/app/nav.ts` already defines 15 sections; the hub proposes 5 cards. That is a
consolidation, and the two it drops (`watchlist`, `news`) are where the catalyst work lives.
Recommendation: hub as the landing page **above** the rail, not instead of it. Also, the
Scanner card's "34 hits today" should show the single strongest current signal instead of a
count — a count is what the alerts table already produces 25,000 times a day, unread.
