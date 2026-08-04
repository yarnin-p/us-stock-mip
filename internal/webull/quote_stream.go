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

type BookQuoteHandler func(context.Context, BookQuote) error

// StreamQuotes connects to Webull's realtime QUOTE topic and delivers the
// ordered bid/ask book supplied by the market-data stream.
func (client *Client) StreamQuotes(
	ctx context.Context,
	brokerURL string,
	symbols []string,
	handler BookQuoteHandler,
) error {
	if handler == nil {
		return errors.New("webull book-quote handler is required")
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
	books := make(chan BookQuote, 256)
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
		if message.Topic() != "quote" {
			return
		}
		quote, decodeErr := decodeQuote(message.Payload())
		if decodeErr != nil {
			select {
			case streamErrors <- decodeErr:
			default:
			}
			return
		}
		select {
		case books <- quote:
		default:
			select {
			case streamErrors <- errors.New("webull book-quote buffer is full"):
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
				subscribeErr := client.SubscribeQuotes(
					subscribeContext,
					sessionID,
					symbols,
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
				context.Background(),
				5*time.Second,
			)
			_ = client.UnsubscribeAll(unsubscribeContext, sessionID)
			cancel()
			return nil
		case err := <-streamErrors:
			return err
		case quote := <-books:
			if err := handler(ctx, quote); err != nil {
				return err
			}
		}
	}
}
