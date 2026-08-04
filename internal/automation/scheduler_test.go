package automation

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type memoryStore struct {
	mu       sync.Mutex
	schedule ScheduleState
	started  int
	finished []string
}

func (store *memoryStore) SaveSchedule(_ context.Context, state ScheduleState) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.schedule = state
	return nil
}

func (store *memoryStore) StartRun(
	_ context.Context, _ string, _ time.Time, _ int,
) (int64, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.started++
	return int64(store.started), nil
}

func (store *memoryStore) FinishRun(
	_ context.Context, _ int64, status, _ string,
) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.finished = append(store.finished, status)
	return nil
}

func TestNextUsesConfiguredThailandTimezoneAndSkipsWeekend(t *testing.T) {
	location, err := time.LoadLocation("Asia/Bangkok")
	if err != nil {
		t.Fatal(err)
	}
	scheduler, err := New(Config{
		Location: location,
		Jobs:     []Job{{Name: "scan", Hour: 15, Minute: 0}},
	})
	if err != nil {
		t.Fatal(err)
	}

	fridayAfterRun := time.Date(2026, 7, 31, 15, 1, 0, 0, location)
	next := scheduler.Next(fridayAfterRun)

	if got := next.At.Format(time.RFC3339); got != "2026-08-03T15:00:00+07:00" {
		t.Fatalf("next run = %s", got)
	}
	if next.Job.Name != "scan" {
		t.Fatalf("next job = %q", next.Job.Name)
	}
}

func TestNextHonorsTradingCalendar(t *testing.T) {
	location := time.FixedZone("ICT", 7*60*60)
	scheduler, err := New(Config{
		Location: location,
		IsTradingDay: func(date time.Time) bool {
			return date.Format(time.DateOnly) != "2026-12-25" &&
				date.Weekday() != time.Saturday &&
				date.Weekday() != time.Sunday
		},
		Jobs: []Job{{Name: "scan", Hour: 15}},
	})
	if err != nil {
		t.Fatal(err)
	}
	next := scheduler.Next(time.Date(2026, 12, 25, 8, 0, 0, 0, location))
	if got := next.At.Format(time.DateOnly); got != "2026-12-28" {
		t.Fatalf("next trading date = %s", got)
	}
}

func TestExecuteRetriesAndRecordsSuccess(t *testing.T) {
	location := time.FixedZone("ICT", 7*60*60)
	store := &memoryStore{}
	attempts := 0
	scheduler, err := New(Config{
		Location: location,
		Store:    store,
		Retry: RetryPolicy{
			MaximumAttempts: 3,
			InitialDelay:    time.Millisecond,
		},
		Jobs: []Job{{
			Name: "scan", Hour: 15,
			Run: func(context.Context) error {
				attempts++
				if attempts < 3 {
					return errors.New("temporary failure")
				}
				return nil
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	err = scheduler.Execute(
		context.Background(),
		ScheduledJob{
			Job: scheduler.jobs[0],
			At:  time.Date(2026, 7, 29, 15, 0, 0, 0, location),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d", attempts)
	}
	if len(store.finished) != 3 ||
		store.finished[0] != StatusRetrying ||
		store.finished[2] != StatusSucceeded {
		t.Fatalf("finished statuses = %#v", store.finished)
	}
}

func TestParseTimeRejectsInvalidClock(t *testing.T) {
	if _, _, _, err := ParseTime("25:00"); err == nil {
		t.Fatal("expected invalid clock error")
	}
	hour, minute, second, err := ParseTime("15:01:30")
	if err != nil {
		t.Fatal(err)
	}
	if hour != 15 || minute != 1 || second != 30 {
		t.Fatalf("parsed = %d:%d:%d", hour, minute, second)
	}
}
