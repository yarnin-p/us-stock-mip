package dashboard

import (
	"context"
	"testing"
	"time"
)

type eventSourceStub struct {
	events chan Event
}

func (source *eventSourceStub) Events(context.Context) (<-chan Event, error) {
	return source.events, nil
}

func TestEventHubFansOutOneSource(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	source := &eventSourceStub{events: make(chan Event, 4)}
	invalidated := make(chan Event, 1)
	hub := NewEventHub(ctx, source, func(event Event) {
		invalidated <- event
	})
	first, err := hub.Events(ctx)
	if err != nil {
		t.Fatal(err)
	}
	second, err := hub.Events(ctx)
	if err != nil {
		t.Fatal(err)
	}

	event := Event{Scope: "scan", Operation: "INSERT", OccurredAt: time.Now()}
	source.events <- event

	for name, channel := range map[string]<-chan Event{
		"first": first, "second": second, "invalidation": invalidated,
	} {
		select {
		case received := <-channel:
			if received.Scope != "scan" {
				t.Fatalf("%s received scope %q", name, received.Scope)
			}
		case <-time.After(time.Second):
			t.Fatalf("%s did not receive event", name)
		}
	}
}
