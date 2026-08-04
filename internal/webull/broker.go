package webull

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Account struct {
	ID   string `json:"account_id"`
	Type string `json:"account_type"`
}

type AccountBalance struct {
	AccountID      string
	Currency       string
	BuyingPower    float64
	NetLiquidation float64
	DayPnL         float64
	UnrealizedPnL  float64
}

type BrokerPosition struct {
	AccountID     string
	PositionID    string
	Symbol        string
	Quantity      float64
	AveragePrice  *float64
	UnrealizedPnL *float64
}

type BrokerOrder struct {
	AccountID      string
	ClientOrderID  string
	OrderID        string
	Symbol         string
	Side           string
	Status         string
	TotalQuantity  float64
	FilledQuantity float64
	FilledPrice    *float64
	Commission     float64
	Fees           float64
	PlacedAt       *time.Time
	FilledAt       *time.Time
}

type rawBrokerOrderGroup struct {
	ClientOrderID string           `json:"client_order_id"`
	Orders        []rawBrokerOrder `json:"orders"`
}

type rawBrokerOrder struct {
	ClientOrderID  string `json:"client_order_id"`
	OrderID        string `json:"order_id"`
	Symbol         string `json:"symbol"`
	Side           string `json:"side"`
	Status         string `json:"status"`
	TotalQuantity  string `json:"total_quantity"`
	FilledQuantity string `json:"filled_quantity"`
	FilledPrice    string `json:"filled_price"`
	PlaceTimeAt    string `json:"place_time_at"`
	FilledTimeAt   string `json:"filled_time_at"`
	Commission     struct {
		Actual string `json:"actual_commission"`
	} `json:"commission"`
	Fees []struct {
		Actual string `json:"actual_value"`
	} `json:"fees"`
}

func (client *Client) Accounts(ctx context.Context) ([]Account, error) {
	var result []Account
	if err := client.signedGET(
		ctx, "/openapi/account/list", nil, &result,
	); err != nil {
		return nil, err
	}
	return result, nil
}

func (client *Client) AccountPositions(
	ctx context.Context, accountID string,
) ([]BrokerPosition, error) {
	if strings.TrimSpace(accountID) == "" {
		return nil, errors.New("webull account ID is required")
	}
	query := url.Values{"account_id": []string{accountID}}
	var raw []struct {
		PositionID   string `json:"position_id"`
		Symbol       string `json:"symbol"`
		Quantity     string `json:"quantity"`
		AveragePrice string `json:"average_price"`
		CostPrice    string `json:"cost_price"`
		Unrealized   string `json:"unrealized_profit_loss"`
	}
	if err := client.signedGET(
		ctx, "/openapi/assets/positions", query, &raw,
	); err != nil {
		return nil, err
	}
	result := make([]BrokerPosition, 0, len(raw))
	for _, item := range raw {
		quantity, err := parseNumber("broker position quantity", item.Quantity)
		if err != nil {
			return nil, err
		}
		averageRaw := item.AveragePrice
		if averageRaw == "" {
			averageRaw = item.CostPrice
		}
		average, err := optionalNumber("broker average price", averageRaw)
		if err != nil {
			return nil, err
		}
		unrealized, err := optionalNumber("broker unrealized PnL", item.Unrealized)
		if err != nil {
			return nil, err
		}
		result = append(result, BrokerPosition{
			AccountID: accountID, PositionID: item.PositionID,
			Symbol: strings.ToUpper(item.Symbol), Quantity: quantity,
			AveragePrice: average, UnrealizedPnL: unrealized,
		})
	}
	return result, nil
}

func (client *Client) Balance(
	ctx context.Context, accountID string,
) (AccountBalance, error) {
	if strings.TrimSpace(accountID) == "" {
		return AccountBalance{}, errors.New("webull account ID is required")
	}
	query := url.Values{"account_id": []string{accountID}}
	var raw struct {
		TotalAssetCurrency  string `json:"total_asset_currency"`
		TotalCash           string `json:"total_cash_balance"`
		TotalMarketValue    string `json:"total_market_value"`
		TotalNetLiquidation string `json:"total_net_liquidation_value"`
		TotalDayPnL         string `json:"total_day_profit_loss"`
		TotalUnrealizedPnL  string `json:"total_unrealized_profit_loss"`
		Assets              []struct {
			Currency       string `json:"currency"`
			CashBalance    string `json:"cash_balance"`
			MarketValue    string `json:"market_value"`
			BuyingPower    string `json:"buying_power"`
			NetLiquidation string `json:"net_liquidation_value"`
			DayPnL         string `json:"day_profit_loss"`
			UnrealizedPnL  string `json:"unrealized_profit_loss"`
		} `json:"account_currency_assets"`
	}
	if err := client.signedGET(
		ctx, "/openapi/assets/balance", query, &raw,
	); err != nil {
		return AccountBalance{}, err
	}
	buyingPower := 0.0
	assetNet := 0.0
	assetValue := 0.0
	assetDayPnL := 0.0
	assetUnrealizedPnL := 0.0
	for _, asset := range raw.Assets {
		if asset.Currency != "" && asset.Currency != "USD" {
			continue
		}
		value, err := optionalNumber("account buying power", asset.BuyingPower)
		if err != nil {
			return AccountBalance{}, err
		}
		buyingPower += numberOrZero(value)
		value, err = optionalNumber(
			"account net liquidation", asset.NetLiquidation,
		)
		if err != nil {
			return AccountBalance{}, err
		}
		assetNet += numberOrZero(value)
		cash, err := optionalNumber("account cash balance", asset.CashBalance)
		if err != nil {
			return AccountBalance{}, err
		}
		marketValue, err := optionalNumber(
			"account market value", asset.MarketValue,
		)
		if err != nil {
			return AccountBalance{}, err
		}
		assetValue += numberOrZero(cash) + numberOrZero(marketValue)
		dayPnL, err := optionalNumber("account day PnL", asset.DayPnL)
		if err != nil {
			return AccountBalance{}, err
		}
		unrealizedPnL, err := optionalNumber(
			"account unrealized PnL", asset.UnrealizedPnL,
		)
		if err != nil {
			return AccountBalance{}, err
		}
		assetDayPnL += numberOrZero(dayPnL)
		assetUnrealizedPnL += numberOrZero(unrealizedPnL)
	}
	netLiquidation, err := optionalNumber(
		"total net liquidation", raw.TotalNetLiquidation,
	)
	if err != nil {
		return AccountBalance{}, err
	}
	net := numberOrZero(netLiquidation)
	currency := strings.ToUpper(strings.TrimSpace(raw.TotalAssetCurrency))
	if currency != "" && currency != "USD" {
		net = assetNet
		if net == 0 {
			net = assetValue
		}
		currency = "USD"
	}
	if net == 0 {
		cash, err := optionalNumber("total cash", raw.TotalCash)
		if err != nil {
			return AccountBalance{}, err
		}
		marketValue, err := optionalNumber(
			"total market value", raw.TotalMarketValue,
		)
		if err != nil {
			return AccountBalance{}, err
		}
		net = numberOrZero(cash) + numberOrZero(marketValue)
		if net == 0 {
			net = assetNet
		}
		if net == 0 {
			net = assetValue
		}
	}
	if currency == "" {
		currency = "USD"
	}
	dayPnL, err := optionalNumber("total day PnL", raw.TotalDayPnL)
	if err != nil {
		return AccountBalance{}, err
	}
	unrealizedPnL, err := optionalNumber(
		"total unrealized PnL", raw.TotalUnrealizedPnL,
	)
	if err != nil {
		return AccountBalance{}, err
	}
	dayPnLValue := numberOrZero(dayPnL)
	unrealizedPnLValue := numberOrZero(unrealizedPnL)
	if raw.TotalAssetCurrency != "" &&
		!strings.EqualFold(raw.TotalAssetCurrency, "USD") {
		dayPnLValue = assetDayPnL
		unrealizedPnLValue = assetUnrealizedPnL
	}
	return AccountBalance{
		AccountID: accountID, Currency: currency,
		BuyingPower: buyingPower, NetLiquidation: net,
		DayPnL: dayPnLValue, UnrealizedPnL: unrealizedPnLValue,
	}, nil
}

func (client *Client) OrderHistory(
	ctx context.Context, accountID string,
) ([]BrokerOrder, error) {
	if strings.TrimSpace(accountID) == "" {
		return nil, errors.New("webull account ID is required")
	}
	query := url.Values{
		"account_id": []string{accountID},
		"page_size":  []string{"100"},
	}
	var groups []rawBrokerOrderGroup
	if err := client.signedGET(
		ctx, "/openapi/trade/order/history", query, &groups,
	); err != nil {
		return nil, err
	}
	return parseBrokerOrderGroups(accountID, groups)
}

func (client *Client) OrderDetail(
	ctx context.Context, accountID, clientOrderID string,
) (BrokerOrder, error) {
	if strings.TrimSpace(accountID) == "" ||
		strings.TrimSpace(clientOrderID) == "" {
		return BrokerOrder{}, errors.New(
			"webull account and client order IDs are required",
		)
	}
	query := url.Values{
		"account_id":      []string{accountID},
		"client_order_id": []string{clientOrderID},
	}
	var group rawBrokerOrderGroup
	if err := client.signedGET(
		ctx, "/openapi/trade/order/detail", query, &group,
	); err != nil {
		return BrokerOrder{}, err
	}
	orders, err := parseBrokerOrderGroups(
		accountID, []rawBrokerOrderGroup{group},
	)
	if err != nil {
		return BrokerOrder{}, err
	}
	if len(orders) != 1 {
		return BrokerOrder{}, fmt.Errorf(
			"webull order detail returned %d orders", len(orders),
		)
	}
	return orders[0], nil
}

func parseBrokerOrderGroups(
	accountID string, groups []rawBrokerOrderGroup,
) ([]BrokerOrder, error) {
	result := make([]BrokerOrder, 0)
	for _, group := range groups {
		for _, item := range group.Orders {
			total, err := optionalNumber("order total quantity", item.TotalQuantity)
			if err != nil {
				return nil, err
			}
			filled, err := optionalNumber("order filled quantity", item.FilledQuantity)
			if err != nil {
				return nil, err
			}
			price, err := optionalNumber("order filled price", item.FilledPrice)
			if err != nil {
				return nil, err
			}
			commission, err := optionalNumber(
				"order commission", item.Commission.Actual,
			)
			if err != nil {
				return nil, err
			}
			fees := 0.0
			for _, rawFee := range item.Fees {
				fee, err := optionalNumber("order fee", rawFee.Actual)
				if err != nil {
					return nil, err
				}
				fees += numberOrZero(fee)
			}
			result = append(result, BrokerOrder{
				AccountID: accountID, ClientOrderID: item.ClientOrderID,
				OrderID: item.OrderID, Symbol: strings.ToUpper(item.Symbol),
				Side: item.Side, Status: item.Status,
				TotalQuantity:  numberOrZero(total),
				FilledQuantity: numberOrZero(filled), FilledPrice: price,
				Commission: numberOrZero(commission), Fees: fees,
				PlacedAt: parseOptionalTime(item.PlaceTimeAt),
				FilledAt: parseOptionalTime(item.FilledTimeAt),
			})
		}
	}
	return result, nil
}

func (client *Client) signedGET(
	ctx context.Context, path string, query url.Values, target any,
) (_ error) {
	if client.currentAccessToken() == "" {
		return errors.New("webull access token is required")
	}
	endpoint := client.baseURL.JoinPath(strings.TrimPrefix(path, "/"))
	if query == nil {
		query = make(url.Values)
	}
	endpoint.RawQuery = query.Encode()
	timestamp := client.clock().UTC().Format(time.RFC3339)
	nonce, err := client.nonce()
	if err != nil {
		return fmt.Errorf("generating Webull nonce: %w", err)
	}
	request, err := http.NewRequestWithContext(
		ctx, http.MethodGet, endpoint.String(), nil,
	)
	if err != nil {
		return fmt.Errorf("creating Webull broker request: %w", err)
	}
	signature, err := client.signer.Signature(
		request.URL.EscapedPath(),
		request.URL.Query(),
		nil,
		request.URL.Host,
		timestamp,
		nonce,
	)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("x-app-key", client.signer.appKey)
	request.Header.Set("x-timestamp", timestamp)
	request.Header.Set("x-signature-algorithm", client.signer.algorithm)
	request.Header.Set("x-signature-version", "1.0")
	request.Header.Set("x-signature-nonce", nonce)
	request.Header.Set("x-version", "v2")
	request.Header.Set("x-signature", signature)
	request.Header.Set("x-access-token", client.currentAccessToken())
	response, err := client.http.Do(request)
	if err != nil {
		return fmt.Errorf("sending Webull broker request: %w", err)
	}
	if response.StatusCode == http.StatusOK {
		decodeErr := json.NewDecoder(
			io.LimitReader(response.Body, maxResponseSize),
		).Decode(target)
		_ = response.Body.Close()
		if decodeErr != nil {
			return fmt.Errorf("decoding Webull broker response: %w", decodeErr)
		}
		return nil
	}
	message, _ := io.ReadAll(io.LimitReader(response.Body, 8<<10))
	_ = response.Body.Close()
	return fmt.Errorf(
		"webull broker returned HTTP %d: %s",
		response.StatusCode, strings.TrimSpace(string(message)),
	)
}

func optionalNumber(name, raw string) (*float64, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return nil, fmt.Errorf("parsing Webull %s: %w", name, err)
	}
	return &value, nil
}

func numberOrZero(value *float64) float64 {
	if value == nil {
		return 0
	}
	return *value
}

func parseOptionalTime(raw string) *time.Time {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	value, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return nil
	}
	value = value.UTC()
	return &value
}
