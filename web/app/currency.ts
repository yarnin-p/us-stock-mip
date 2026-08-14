"use client";

/* Which currency the money figures are shown in, shared by every screen.
 *
 * Scope, stated once so it is not guessed at elsewhere: this switches the amounts
 * that are *yours* -- what a position risks, what it returns, what it costs. It does
 * not touch prices. Entry, stop and target stay in dollars because that is what the
 * market quotes and what the broker screen shows, and a stop written in baht is a
 * number you cannot check against Webull.
 *
 * The rate is one number in one place. It should come from `usd_thb` in the preview
 * response once the API carries it; until then it is editable and defaults to 33.60,
 * which is the design's fixture. A converted figure is only as honest as its rate, so
 * the rate is shown next to the total rather than hidden behind it.
 */

import { useCallback, useEffect, useState } from "react";

export type Currency = "THB" | "USD";

const KEY = "mip.currency";
const RATE_KEY = "mip.usdthb";
export const DEFAULT_RATE = 33.6;

export const CURRENCY_SYMBOL: Record<Currency, string> = { THB: "฿", USD: "$" };

/* Baht is shown whole. A risk figure of ฿664.83 invites a precision that the rate
 * behind it does not have, and the satang has never changed a decision. Dollars keep
 * their cents because the fee model works in them. */
export function formatMoney(usd: number, currency: Currency, rate: number): string {
  if (!Number.isFinite(usd)) return CURRENCY_SYMBOL[currency] + "0";
  if (currency === "USD") {
    return (
      "$" +
      usd.toLocaleString("en-US", { minimumFractionDigits: 2, maximumFractionDigits: 2 })
    );
  }
  return "฿" + Math.round(usd * rate).toLocaleString("en-US");
}

/* Reads from localStorage after mount, never during render: the server cannot know
 * what is stored in a browser, so seeding state from it would put the first client
 * pass out of step with the server HTML. */
export function useCurrency() {
  const [currency, setCurrencyState] = useState<Currency>("THB");
  const [rate, setRateState] = useState(DEFAULT_RATE);

  useEffect(() => {
    const stored = window.localStorage.getItem(KEY);
    if (stored === "THB" || stored === "USD") setCurrencyState(stored);
    const storedRate = Number(window.localStorage.getItem(RATE_KEY));
    if (storedRate > 0) setRateState(storedRate);
  }, []);

  // Every screen in the app listens, so switching on the hub is already switched by
  // the time the terminal renders -- and a second tab follows through `storage`.
  useEffect(() => {
    const onChange = (event: Event) => {
      const detail = (event as CustomEvent<{ currency: Currency; rate: number }>).detail;
      if (detail?.currency) setCurrencyState(detail.currency);
      if (detail?.rate) setRateState(detail.rate);
    };
    const onStorage = (event: StorageEvent) => {
      if (event.key === KEY && (event.newValue === "THB" || event.newValue === "USD")) {
        setCurrencyState(event.newValue);
      }
      if (event.key === RATE_KEY && Number(event.newValue) > 0) {
        setRateState(Number(event.newValue));
      }
    };
    window.addEventListener("mip:currency", onChange);
    window.addEventListener("storage", onStorage);
    return () => {
      window.removeEventListener("mip:currency", onChange);
      window.removeEventListener("storage", onStorage);
    };
  }, []);

  const publish = useCallback((next: Currency, nextRate: number) => {
    window.localStorage.setItem(KEY, next);
    window.localStorage.setItem(RATE_KEY, String(nextRate));
    window.dispatchEvent(
      new CustomEvent("mip:currency", { detail: { currency: next, rate: nextRate } }),
    );
  }, []);

  const setCurrency = useCallback(
    (next: Currency) => {
      setCurrencyState(next);
      publish(next, rate);
    },
    [publish, rate],
  );

  const setRate = useCallback(
    (next: number) => {
      if (!(next > 0)) return;
      setRateState(next);
      publish(currency, next);
    },
    [publish, currency],
  );

  const format = useCallback(
    (usd: number) => formatMoney(usd, currency, rate),
    [currency, rate],
  );

  return { currency, setCurrency, rate, setRate, format, symbol: CURRENCY_SYMBOL[currency] };
}
