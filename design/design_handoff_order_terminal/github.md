# Source repository

repo: yarnin-p/us-stock-mip
branch: main
path: web/

## Last sync

date: 2026-08-14T05:02:18Z

### Updated in this project
- Read `web/app/TicketPanel.tsx` + `web/app/ticket/page.tsx` to align the redesign with the real ticket model (preview/submit, no draft persistence).
- Confirmed the app is Next 16 + plain CSS classes in `web/app/globals.css` (`tk-*`), not Tailwind components.
- Design files remain independent DCs; no repository UI was recreated or overwritten.

## Screen map

| Project screen | Repo files it maps to |
| --- | --- |
| Terminal-H-PopBlocks.dc.html | web/app/TicketPanel.tsx, web/app/ticket/page.tsx, web/app/globals.css |
| Portal Hub.dc.html | web/app/MomentumDashboard.tsx, web/app/page.tsx, web/app/layout.tsx |
| Terminal-G-Themes.dc.html | web/app/TicketPanel.tsx (theme exploration only) |

## Notes for the next sync

- Ticket API contract: `POST /ticket/preview`, `POST /ticket/submit` (`send: true`); ticket fields `ticker, side, entry, stop, target, shares, notional, actual_risk, risk_per_share, reward_risk, stop_distance, capped_by`.
- The repo sizes by **shares** (with +/- steppers) and shows risk in **THB** via `usd_thb`; the design currently sizes by cash and shows USD only.
- The repo has **BUY/SELL side tabs** and a **confirm-then-send** review step; the design has a single Save action.
- No draft persistence exists upstream — hub copy was corrected to match.
