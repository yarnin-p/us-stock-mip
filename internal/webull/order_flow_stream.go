package webull

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

type TickHandler func(context.Context, Tick) error

// StreamOrderFlow uses one Webull MQTT session for both the realtime order
// book and the Webull-provided tick feed. Subscriptions are replayed after
// reconnect because they are bound to the MQTT session ID.
func (client *Client) StreamOrderFlow(
	ctx context.Context,
	brokerURL string,
	symbols []string,
	quoteHandler BookQuoteHandler,
	tickHandler TickHandler,
) error {
	if quoteHandler == nil || tickHandler == nil {
		return errors.New("webull quote and tick handlers are required")
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
	type event struct {
		quote *BookQuote
		tick  *Tick
	}
	events := make(chan event, 512)
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
		var item event
		switch message.Topic() {
		case "quote":
			value, decodeErr := decodeQuote(message.Payload())
			if decodeErr != nil {
				// Webull can publish a transient one-sided book while a
				// session changes. It is not executable and must not force a
				// reconnect of an otherwise healthy MQTT session.
				return
			}
			item.quote = &value
		case "tick":
			value, decodeErr := decodeTick(message.Payload())
			if decodeErr != nil {
				// A malformed market event is isolated from the connection.
				// Subsequent valid events remain usable.
				return
			}
			item.tick = &value
		default:
			return
		}
		select {
		case events <- item:
		default:
			reportStreamError(
				streamErrors,
				errors.New("webull order-flow buffer is full"),
			)
		}
	})
	options.SetOnConnectHandler(func(_ mqtt.Client) {
		generation := subscriptionGeneration.Add(1)
		go func() {
			backoff := time.Second
			const maximumAttempts = 3
			attempt := 0
			var lastErr error
			for subscriptionGeneration.Load() == generation {
				attempt++
				subscribeContext, cancel := context.WithTimeout(
					ctx, 15*time.Second,
				)
				lastErr = client.SubscribeOrderFlow(
					subscribeContext, sessionID, symbols,
				)
				cancel()
				// A partial subscription is a working stream missing some symbols, not a
				// failed one. Retrying it would re-subscribe the symbols that already
				// took and keep failing on the ones that cannot, forever.
				if IsPartialSubscription(lastErr) {
					lastErr = nil
				}
				if lastErr == nil {
					select {
					case subscribed <- struct{}{}:
					default:
					}
					return
				}
				if attempt >= maximumAttempts {
					reportStreamError(
						streamErrors,
						fmt.Errorf(
							"subscribing to Webull order flow after %d attempts: %w",
							attempt,
							lastErr,
						),
					)
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
	options.SetConnectionLostHandler(func(_ mqtt.Client, _ error) {
		subscriptionGeneration.Add(1)
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
		case item := <-events:
			if item.quote != nil {
				if err := quoteHandler(ctx, *item.quote); err != nil {
					return err
				}
			} else if item.tick != nil {
				if err := tickHandler(ctx, *item.tick); err != nil {
					return err
				}
			}
		}
	}
}

func reportStreamError(destination chan<- error, err error) {
	select {
	case destination <- err:
	default:
	}
}
