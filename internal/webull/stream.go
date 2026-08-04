package webull

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

type SnapshotHandler func(context.Context, Snapshot) error

// StreamSnapshots connects to Webull's MQTT broker. The HTTP subscription is
// repeated after every reconnect because Webull binds subscriptions to the
// MQTT client/session ID.
func (client *Client) StreamSnapshots(
	ctx context.Context,
	brokerURL string,
	symbols []string,
	handler SnapshotHandler,
) error {
	if handler == nil {
		return errors.New("webull snapshot handler is required")
	}
	broker, err := url.Parse(strings.TrimSpace(brokerURL))
	if err != nil || (broker.Scheme != "ssl" && broker.Scheme != "wss") ||
		broker.Host == "" {
		return errors.New("WEBULL_MQTT_URL must be an ssl:// or wss:// URL")
	}
	sessionID, err := randomNonce()
	if err != nil {
		return fmt.Errorf("generating Webull streaming session: %w", err)
	}
	password, err := randomNonce()
	if err != nil {
		return fmt.Errorf("generating Webull MQTT password: %w", err)
	}
	quotes := make(chan Snapshot, 256)
	streamErrors := make(chan error, 8)
	subscribed := make(chan struct{}, 1)
	options := mqtt.NewClientOptions().
		AddBroker(brokerURL).
		SetClientID(sessionID).
		SetUsername(client.signer.appKey).
		SetPassword(password).
		SetTLSConfig(&tls.Config{
			MinVersion: tls.VersionTLS12, ServerName: broker.Hostname(),
		}).
		SetAutoReconnect(true).
		SetConnectRetry(false).
		SetOrderMatters(false)
	var subscriptionGeneration atomic.Uint64
	options.SetDefaultPublishHandler(func(_ mqtt.Client, message mqtt.Message) {
		if message.Topic() != "snapshot" {
			return
		}
		snapshot, decodeErr := decodeSnapshot(message.Payload())
		if decodeErr != nil {
			select {
			case streamErrors <- decodeErr:
			default:
			}
			return
		}
		select {
		case quotes <- snapshot:
		default:
			select {
			case streamErrors <- errors.New("webull quote buffer is full"):
			default:
			}
		}
	})
	options.SetOnConnectHandler(func(_ mqtt.Client) {
		generation := subscriptionGeneration.Add(1)
		go func() {
			backoff := time.Second
			for subscriptionGeneration.Load() == generation {
				subscribeContext, cancel := context.WithTimeout(ctx, 15*time.Second)
				subscribeErr := client.SubscribeSnapshots(
					subscribeContext, sessionID, symbols,
				)
				cancel()
				if subscribeErr == nil {
					select {
					case subscribed <- struct{}{}:
					default:
					}
					return
				}
				timer := time.NewTimer(backoff)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
				backoff = min(backoff*2, 30*time.Second)
			}
		}()
	})
	options.SetConnectionLostHandler(func(_ mqtt.Client, lostErr error) {
		// Paho reconnects automatically. OnConnect replays the HTTP
		// subscription for this session after the broker is available again.
		subscriptionGeneration.Add(1)
		_ = lostErr
	})
	mqttClient := mqtt.NewClient(options)
	connectToken := mqttClient.Connect()
	connectDeadline := time.NewTimer(20 * time.Second)
	defer connectDeadline.Stop()
	for !connectToken.WaitTimeout(250 * time.Millisecond) {
		select {
		case <-ctx.Done():
			mqttClient.Disconnect(0)
			return context.Cause(ctx)
		case <-connectDeadline.C:
			mqttClient.Disconnect(0)
			return errors.New("timed out connecting to Webull MQTT")
		default:
		}
	}
	if err := connectToken.Error(); err != nil {
		return fmt.Errorf("connecting to Webull MQTT: %w", err)
	}
	defer mqttClient.Disconnect(250)
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case err := <-streamErrors:
		return err
	case <-subscribed:
	}
	for {
		select {
		case <-ctx.Done():
			unsubscribeContext, cancel := context.WithTimeout(
				context.Background(), 5*time.Second,
			)
			_ = client.UnsubscribeAll(unsubscribeContext, sessionID)
			cancel()
			return nil
		case err := <-streamErrors:
			return err
		case snapshot := <-quotes:
			if err := handler(ctx, snapshot); err != nil {
				return err
			}
		}
	}
}

func decodeSnapshot(payload []byte) (Snapshot, error) {
	fields, err := protobufStrings(payload)
	if err != nil {
		return Snapshot{}, fmt.Errorf("decoding Webull snapshot protobuf: %w", err)
	}
	basicFields, err := protobufStrings([]byte(fields[1]))
	if err != nil {
		return Snapshot{}, fmt.Errorf("decoding Webull snapshot basic protobuf: %w", err)
	}
	price, err := parseNumber("stream price", fields[3])
	if err != nil {
		return Snapshot{}, err
	}
	volume, err := parseNumber("stream volume", fields[8])
	if err != nil {
		return Snapshot{}, err
	}
	changeRatio, err := parseNumber("stream change ratio", fields[10])
	if err != nil {
		return Snapshot{}, err
	}
	previousClose, err := parseNumber("stream previous close", fields[7])
	if err != nil {
		return Snapshot{}, err
	}
	timestampRaw := fields[2]
	if timestampRaw == "" {
		timestampRaw = basicFields[3]
	}
	timestamp, err := strconv.ParseInt(timestampRaw, 10, 64)
	if err != nil {
		return Snapshot{}, fmt.Errorf("parsing Webull stream timestamp: %w", err)
	}
	if timestamp < 100_000_000_000 {
		timestamp *= 1000
	}
	snapshot := Snapshot{
		Symbol: strings.ToUpper(strings.TrimSpace(basicFields[1])),
		Price:  price, Volume: volume, ChangeRatio: changeRatio,
		PreviousClose: previousClose, ObservedAt: time.UnixMilli(timestamp).UTC(),
	}
	if !symbolPattern.MatchString(snapshot.Symbol) || snapshot.ObservedAt.IsZero() {
		return Snapshot{}, errors.New("invalid Webull stream snapshot")
	}
	return snapshot, nil
}

func decodeQuote(payload []byte) (BookQuote, error) {
	fields, err := protobufRepeatedStrings(payload)
	if err != nil {
		return BookQuote{}, fmt.Errorf("decoding Webull quote protobuf: %w", err)
	}
	if len(fields[1]) != 1 {
		return BookQuote{}, errors.New("webull quote basic data is missing")
	}
	basicFields, err := protobufStrings([]byte(fields[1][0]))
	if err != nil {
		return BookQuote{}, fmt.Errorf("decoding Webull quote basic protobuf: %w", err)
	}
	timestamp, err := strconv.ParseInt(basicFields[3], 10, 64)
	if err != nil {
		return BookQuote{}, fmt.Errorf("parsing Webull quote timestamp: %w", err)
	}
	if timestamp < 100_000_000_000 {
		timestamp *= 1000
	}
	quote := BookQuote{
		Symbol:     strings.ToUpper(strings.TrimSpace(basicFields[1])),
		Asks:       make([]BookLevel, 0, len(fields[2])),
		Bids:       make([]BookLevel, 0, len(fields[3])),
		ObservedAt: time.UnixMilli(timestamp).UTC(),
	}
	for _, raw := range fields[2] {
		level, err := decodeBookLevel([]byte(raw))
		if err != nil {
			return BookQuote{}, fmt.Errorf("decoding Webull ask: %w", err)
		}
		quote.Asks = append(quote.Asks, level)
	}
	for _, raw := range fields[3] {
		level, err := decodeBookLevel([]byte(raw))
		if err != nil {
			return BookQuote{}, fmt.Errorf("decoding Webull bid: %w", err)
		}
		quote.Bids = append(quote.Bids, level)
	}
	if !symbolPattern.MatchString(quote.Symbol) || quote.ObservedAt.IsZero() ||
		len(quote.Asks) == 0 || len(quote.Bids) == 0 {
		return BookQuote{}, errors.New("invalid Webull quote")
	}
	return quote, nil
}

func decodeTick(payload []byte) (Tick, error) {
	fields, err := protobufStrings(payload)
	if err != nil {
		return Tick{}, fmt.Errorf("decoding Webull tick protobuf: %w", err)
	}
	basicFields, err := protobufStrings([]byte(fields[1]))
	if err != nil {
		return Tick{}, fmt.Errorf("decoding Webull tick basic protobuf: %w", err)
	}
	price, err := parseNumber("tick price", fields[3])
	if err != nil {
		return Tick{}, err
	}
	volume, err := parseNumber("tick volume", fields[4])
	if err != nil {
		return Tick{}, err
	}
	// Tick.time is a session clock such as "04:00:30". Basic.timestamp is the
	// exchange event epoch and is the unambiguous source for ordering.
	timestampRaw := basicFields[3]
	if timestampRaw == "" {
		timestampRaw = fields[2]
	}
	timestamp, err := strconv.ParseInt(timestampRaw, 10, 64)
	if err != nil {
		return Tick{}, fmt.Errorf("parsing Webull tick timestamp: %w", err)
	}
	if timestamp < 100_000_000_000 {
		timestamp *= 1000
	}
	tick := Tick{
		Symbol: strings.ToUpper(strings.TrimSpace(basicFields[1])),
		Price:  price, Volume: volume,
		Side:       strings.ToUpper(strings.TrimSpace(fields[5])),
		EventID:    fmt.Sprintf("%x", sha256.Sum256(payload)),
		ObservedAt: time.UnixMilli(timestamp).UTC(),
	}
	if !symbolPattern.MatchString(tick.Symbol) || tick.Price <= 0 ||
		tick.Volume < 0 || tick.ObservedAt.IsZero() {
		return Tick{}, errors.New("invalid Webull tick")
	}
	return tick, nil
}

func decodeBookLevel(payload []byte) (BookLevel, error) {
	fields, err := protobufStrings(payload)
	if err != nil {
		return BookLevel{}, err
	}
	price, err := parseNumber("book price", fields[1])
	if err != nil {
		return BookLevel{}, err
	}
	size, err := parseNumber("book size", fields[2])
	if err != nil {
		return BookLevel{}, err
	}
	if price <= 0 || size < 0 {
		return BookLevel{}, errors.New("invalid Webull book level")
	}
	return BookLevel{Price: price, Size: size}, nil
}

func protobufRepeatedStrings(payload []byte) (map[int][]string, error) {
	fields := make(map[int][]string)
	for len(payload) > 0 {
		key, consumed := consumeVarint(payload)
		if consumed == 0 {
			return nil, errors.New("invalid protobuf field key")
		}
		payload = payload[consumed:]
		fieldNumber, wireType := int(key>>3), int(key&7)
		switch wireType {
		case 0:
			_, consumed = consumeVarint(payload)
			if consumed == 0 {
				return nil, errors.New("invalid protobuf varint")
			}
			payload = payload[consumed:]
		case 1:
			if len(payload) < 8 {
				return nil, errors.New("truncated protobuf fixed64")
			}
			payload = payload[8:]
		case 2:
			length, lengthBytes := consumeVarint(payload)
			if lengthBytes == 0 || length > uint64(len(payload)-lengthBytes) {
				return nil, errors.New("invalid protobuf string length")
			}
			start, end := lengthBytes, lengthBytes+int(length)
			fields[fieldNumber] = append(fields[fieldNumber], string(payload[start:end]))
			payload = payload[end:]
		case 5:
			if len(payload) < 4 {
				return nil, errors.New("truncated protobuf fixed32")
			}
			payload = payload[4:]
		default:
			return nil, fmt.Errorf("unsupported protobuf wire type %d", wireType)
		}
	}
	return fields, nil
}

func protobufStrings(payload []byte) (map[int]string, error) {
	fields := make(map[int]string)
	for len(payload) > 0 {
		key, consumed := consumeVarint(payload)
		if consumed == 0 {
			return nil, errors.New("invalid protobuf field key")
		}
		payload = payload[consumed:]
		fieldNumber, wireType := int(key>>3), int(key&7)
		switch wireType {
		case 0:
			_, consumed = consumeVarint(payload)
			if consumed == 0 {
				return nil, errors.New("invalid protobuf varint")
			}
			payload = payload[consumed:]
		case 1:
			if len(payload) < 8 {
				return nil, errors.New("truncated protobuf fixed64")
			}
			payload = payload[8:]
		case 2:
			length, lengthBytes := consumeVarint(payload)
			if lengthBytes == 0 || length > uint64(len(payload)-lengthBytes) {
				return nil, errors.New("invalid protobuf string length")
			}
			start, end := lengthBytes, lengthBytes+int(length)
			fields[fieldNumber] = string(payload[start:end])
			payload = payload[end:]
		case 5:
			if len(payload) < 4 {
				return nil, errors.New("truncated protobuf fixed32")
			}
			payload = payload[4:]
		default:
			return nil, fmt.Errorf("unsupported protobuf wire type %d", wireType)
		}
	}
	return fields, nil
}

func consumeVarint(payload []byte) (uint64, int) {
	var value uint64
	for index, current := range payload {
		if index == 10 || (index == 9 && current > 1) {
			return 0, 0
		}
		value |= uint64(current&0x7f) << (7 * index)
		if current < 0x80 {
			return value, index + 1
		}
	}
	return 0, 0
}
