package backtest

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"time"
)

type ExitReason string

const (
	ExitStopLoss   ExitReason = "stop_loss"
	ExitTakeProfit ExitReason = "take_profit"
	ExitTime       ExitReason = "time"
)

type Config struct {
	InitialCapital   float64
	PositionFraction float64
	HoldingBars      int
	StopLoss         float64
	TakeProfit       float64
	SlippageBPS      float64
	CommissionBPS    float64
}

type Bar struct {
	Date            time.Time
	Open, High, Low float64
	Close           float64
}

type Signal struct {
	Date  time.Time
	Score float64
}

type Trade struct {
	SignalDate, EntryDate, ExitDate time.Time
	EntryPrice, ExitPrice           float64
	Shares, PNL, Return             float64
	ExitReason                      ExitReason
}

type Result struct {
	Trades                       []Trade
	InitialCapital, FinalCapital float64
	WinRate                      float64
	ProfitFactor                 *float64
	MaxDrawdown, Sharpe          float64
}

type Engine struct{ config Config }

func NewEngine(config Config) (*Engine, error) {
	if config.InitialCapital <= 0 || config.PositionFraction <= 0 || config.PositionFraction > 1 {
		return nil, errors.New("capital and position fraction must be positive and fraction at most one")
	}
	if config.HoldingBars < 1 || config.StopLoss <= 0 || config.StopLoss >= 1 ||
		config.TakeProfit <= 0 || config.SlippageBPS < 0 || config.CommissionBPS < 0 {
		return nil, errors.New("invalid backtest risk or cost configuration")
	}
	return &Engine{config: config}, nil
}

func (engine *Engine) Run(inputBars []Bar, signals []Signal) (Result, error) {
	bars := slices.Clone(inputBars)
	slices.SortFunc(bars, func(a, b Bar) int { return a.Date.Compare(b.Date) })
	if err := validateBars(bars); err != nil {
		return Result{}, err
	}
	if len(bars) < 2 {
		return Result{}, errors.New("backtest requires at least two bars")
	}
	signalByDate := make(map[string]Signal, len(signals))
	for _, signal := range signals {
		if signal.Date.IsZero() || signal.Score < 0 || signal.Score > 1 {
			return Result{}, errors.New("invalid backtest signal")
		}
		signalByDate[dateKey(signal.Date)] = signal
	}

	result := Result{InitialCapital: engine.config.InitialCapital}
	capital := engine.config.InitialCapital
	equityPeak := capital
	var returns []float64
	for index := 0; index+1 < len(bars); index++ {
		signal, ok := signalByDate[dateKey(bars[index].Date)]
		if !ok {
			continue
		}
		entryIndex := index + 1
		entry := bars[entryIndex]
		entryPrice := entry.Open * (1 + engine.config.SlippageBPS/10_000)
		positionValue := capital * engine.config.PositionFraction
		shares := positionValue / entryPrice
		exitIndex := min(entryIndex+engine.config.HoldingBars-1, len(bars)-1)
		exitPrice := bars[exitIndex].Close * (1 - engine.config.SlippageBPS/10_000)
		reason := ExitTime
		stop := entryPrice * (1 - engine.config.StopLoss)
		target := entryPrice * (1 + engine.config.TakeProfit)
		for cursor := entryIndex; cursor <= exitIndex; cursor++ {
			if cursor > entryIndex && bars[cursor].Open <= stop {
				exitIndex = cursor
				exitPrice = bars[cursor].Open * (1 - engine.config.SlippageBPS/10_000)
				reason = ExitStopLoss
				break
			}
			if cursor > entryIndex && bars[cursor].Open >= target {
				exitIndex = cursor
				exitPrice = bars[cursor].Open * (1 - engine.config.SlippageBPS/10_000)
				reason = ExitTakeProfit
				break
			}
			if bars[cursor].Low <= stop {
				exitIndex, exitPrice, reason = cursor, stop, ExitStopLoss
				break
			}
			if bars[cursor].High >= target {
				exitIndex, exitPrice, reason = cursor, target, ExitTakeProfit
				break
			}
		}
		capitalBefore := capital
		cost := (entryPrice + exitPrice) * shares * engine.config.CommissionBPS / 10_000
		pnl := (exitPrice-entryPrice)*shares - cost
		tradeReturn := pnl / positionValue
		entryCost := entryPrice * shares * engine.config.CommissionBPS / 10_000
		for cursor := entryIndex; cursor <= exitIndex; cursor++ {
			markPrice := bars[cursor].Close
			if cursor == exitIndex {
				markPrice = exitPrice
			}
			markCost := entryCost +
				markPrice*shares*engine.config.CommissionBPS/10_000
			equity := capitalBefore + (markPrice-entryPrice)*shares - markCost
			equityPeak = max(equityPeak, equity)
			result.MaxDrawdown = max(
				result.MaxDrawdown,
				(equityPeak-equity)/equityPeak,
			)
		}
		capital = capitalBefore + pnl
		returns = append(returns, tradeReturn)
		result.Trades = append(result.Trades, Trade{
			SignalDate: signal.Date, EntryDate: entry.Date, ExitDate: bars[exitIndex].Date,
			EntryPrice: entryPrice, ExitPrice: exitPrice, Shares: shares, PNL: pnl,
			Return: tradeReturn, ExitReason: reason,
		})
		index = exitIndex
	}
	result.FinalCapital = capital
	result.calculateMetrics(returns, len(bars))
	return result, nil
}

func (result *Result) calculateMetrics(returns []float64, elapsedBars int) {
	var wins, grossProfit, grossLoss, total float64
	for index, value := range returns {
		total += value
		if result.Trades[index].PNL > 0 {
			wins++
			grossProfit += result.Trades[index].PNL
		} else {
			grossLoss -= result.Trades[index].PNL
		}
	}
	if len(returns) == 0 {
		return
	}
	result.WinRate = wins / float64(len(returns))
	if grossLoss > 0 {
		profitFactor := grossProfit / grossLoss
		result.ProfitFactor = &profitFactor
	}
	mean := total / float64(len(returns))
	var variance float64
	for _, value := range returns {
		variance += (value - mean) * (value - mean)
	}
	if len(returns) > 1 {
		deviation := math.Sqrt(variance / float64(len(returns)-1))
		if deviation > 0 {
			tradesPerYear := 252 * float64(len(returns)) / float64(elapsedBars)
			result.Sharpe = mean / deviation * math.Sqrt(tradesPerYear)
		}
	}
}

func validateBars(bars []Bar) error {
	for index, bar := range bars {
		if bar.Date.IsZero() || bar.Open <= 0 || bar.High <= 0 || bar.Low <= 0 ||
			bar.Close <= 0 || bar.High < bar.Low {
			return fmt.Errorf("invalid bar %d", index)
		}
		if index > 0 && !bar.Date.After(bars[index-1].Date) {
			return errors.New("bar dates must be unique")
		}
	}
	return nil
}

func dateKey(value time.Time) string { return value.Format(time.DateOnly) }
