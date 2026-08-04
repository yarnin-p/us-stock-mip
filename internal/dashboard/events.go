package dashboard

import (
	"context"
	"sync"
	"time"
)

// EventHub maintains one upstream LISTEN connection and fans events out to all
// browser sessions. This prevents every SSE client from reserving a database
// connection of its own.
type EventHub struct {
	context   context.Context
	source    EventSource
	onEvent   func(Event)
	mutex     sync.Mutex
	nextID    uint64
	listeners map[uint64]chan Event
}

func NewEventHub(
	ctx context.Context,
	source EventSource,
	onEvent func(Event),
) *EventHub {
	hub := &EventHub{
		context: ctx, source: source, onEvent: onEvent,
		listeners: make(map[uint64]chan Event),
	}
	go hub.run()
	return hub
}

func (hub *EventHub) Events(ctx context.Context) (<-chan Event, error) {
	events := make(chan Event, 32)
	hub.mutex.Lock()
	id := hub.nextID
	hub.nextID++
	hub.listeners[id] = events
	hub.mutex.Unlock()

	go func() {
		select {
		case <-ctx.Done():
		case <-hub.context.Done():
		}
		hub.mutex.Lock()
		if current, ok := hub.listeners[id]; ok {
			delete(hub.listeners, id)
			close(current)
		}
		hub.mutex.Unlock()
	}()
	return events, nil
}

func (hub *EventHub) run() {
	health := time.NewTicker(10 * time.Second)
	defer health.Stop()

	for {
		events, err := hub.source.Events(hub.context)
		if err != nil {
			if !waitForEventHubRetry(hub.context) {
				return
			}
			continue
		}
		for {
			select {
			case <-hub.context.Done():
				hub.close()
				return
			case <-health.C:
				hub.publish(Event{
					Scope: "health", Operation: "heartbeat",
					OccurredAt: time.Now().UTC(),
				})
			case event, open := <-events:
				if !open {
					if !waitForEventHubRetry(hub.context) {
						hub.close()
						return
					}
					goto reconnect
				}
				if hub.onEvent != nil {
					hub.onEvent(event)
				}
				hub.publish(event)
			}
		}
	reconnect:
	}
}

func (hub *EventHub) publish(event Event) {
	hub.mutex.Lock()
	defer hub.mutex.Unlock()
	for _, listener := range hub.listeners {
		select {
		case listener <- event:
		default:
			// A slow browser will refetch on the next event or health heartbeat.
		}
	}
}

func (hub *EventHub) close() {
	hub.mutex.Lock()
	defer hub.mutex.Unlock()
	for id, listener := range hub.listeners {
		delete(hub.listeners, id)
		close(listener)
	}
}

func waitForEventHubRetry(ctx context.Context) bool {
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
