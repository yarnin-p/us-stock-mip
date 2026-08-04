package automation

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	StatusRunning   = "RUNNING"
	StatusRetrying  = "RETRYING"
	StatusSucceeded = "SUCCEEDED"
	StatusFailed    = "FAILED"
)

var ErrAlreadyCompleted = errors.New("automation job already completed")

type Store interface {
	SaveSchedule(context.Context, ScheduleState) error
	StartRun(context.Context, string, time.Time, int) (int64, error)
	FinishRun(context.Context, int64, string, string) error
}

type Job struct {
	Name   string
	Hour   int
	Minute int
	Second int
	Run    func(context.Context) error
}

type ScheduledJob struct {
	Job Job
	At  time.Time
}

type ScheduleState struct {
	Enabled  bool
	Timezone string
	NextJob  string
	NextRun  time.Time
	Updated  time.Time
}

type RetryPolicy struct {
	MaximumAttempts int
	InitialDelay    time.Duration
}

type Config struct {
	Location     *time.Location
	Jobs         []Job
	Retry        RetryPolicy
	Store        Store
	IsTradingDay func(time.Time) bool
}

type Scheduler struct {
	location     *time.Location
	jobs         []Job
	retry        RetryPolicy
	store        Store
	isTradingDay func(time.Time) bool
}

func New(config Config) (*Scheduler, error) {
	if config.Location == nil {
		return nil, errors.New("scheduler location is required")
	}
	if len(config.Jobs) == 0 {
		return nil, errors.New("at least one scheduler job is required")
	}
	jobs := append([]Job(nil), config.Jobs...)
	for index, job := range jobs {
		if strings.TrimSpace(job.Name) == "" {
			return nil, errors.New("scheduler job name is required")
		}
		if job.Hour < 0 || job.Hour > 23 || job.Minute < 0 ||
			job.Minute > 59 || job.Second < 0 || job.Second > 59 {
			return nil, fmt.Errorf("invalid schedule for %s", job.Name)
		}
		if job.Run == nil {
			jobs[index].Run = func(context.Context) error { return nil }
		}
	}
	sort.SliceStable(jobs, func(left, right int) bool {
		leftSeconds := jobs[left].Hour*3600 + jobs[left].Minute*60 + jobs[left].Second
		rightSeconds := jobs[right].Hour*3600 + jobs[right].Minute*60 + jobs[right].Second
		return leftSeconds < rightSeconds
	})
	if config.Retry.MaximumAttempts == 0 {
		config.Retry.MaximumAttempts = 3
	}
	if config.Retry.MaximumAttempts < 1 {
		return nil, errors.New("scheduler maximum attempts must be positive")
	}
	if config.Retry.InitialDelay == 0 {
		config.Retry.InitialDelay = 15 * time.Second
	}
	if config.Retry.InitialDelay < 0 {
		return nil, errors.New("scheduler retry delay must not be negative")
	}
	return &Scheduler{
		location:     config.Location,
		jobs:         jobs,
		retry:        config.Retry,
		store:        config.Store,
		isTradingDay: config.IsTradingDay,
	}, nil
}

func ParseTime(value string) (int, int, int, error) {
	parts := strings.Split(strings.TrimSpace(value), ":")
	if len(parts) != 2 && len(parts) != 3 {
		return 0, 0, 0, fmt.Errorf("invalid scheduler time %q", value)
	}
	values := []int{0, 0, 0}
	for index, part := range parts {
		parsed, err := strconv.Atoi(part)
		if err != nil {
			return 0, 0, 0, fmt.Errorf("invalid scheduler time %q", value)
		}
		values[index] = parsed
	}
	if values[0] < 0 || values[0] > 23 ||
		values[1] < 0 || values[1] > 59 ||
		values[2] < 0 || values[2] > 59 {
		return 0, 0, 0, fmt.Errorf("invalid scheduler time %q", value)
	}
	return values[0], values[1], values[2], nil
}

func (scheduler *Scheduler) Next(now time.Time) ScheduledJob {
	localNow := now.In(scheduler.location)
	for daysAhead := 0; ; daysAhead++ {
		date := localNow.AddDate(0, 0, daysAhead)
		if scheduler.isTradingDay != nil && !scheduler.isTradingDay(date) {
			continue
		}
		if scheduler.isTradingDay == nil &&
			(date.Weekday() == time.Saturday || date.Weekday() == time.Sunday) {
			continue
		}
		for _, job := range scheduler.jobs {
			candidate := time.Date(
				date.Year(), date.Month(), date.Day(),
				job.Hour, job.Minute, job.Second, 0, scheduler.location,
			)
			if !candidate.Before(localNow) {
				return ScheduledJob{Job: job, At: candidate}
			}
		}
	}
}

func (scheduler *Scheduler) Run(ctx context.Context) error {
	for {
		next := scheduler.Next(time.Now())
		if scheduler.store != nil {
			err := scheduler.store.SaveSchedule(ctx, ScheduleState{
				Enabled: true, Timezone: scheduler.location.String(),
				NextJob: next.Job.Name, NextRun: next.At, Updated: time.Now().UTC(),
			})
			if err != nil {
				return fmt.Errorf("saving scheduler state: %w", err)
			}
		}
		timer := time.NewTimer(time.Until(next.At))
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
		if err := scheduler.Execute(ctx, next); err != nil &&
			!errors.Is(err, ErrAlreadyCompleted) {
			// A failed job is recorded and the next scheduled stage must still run.
			continue
		}
	}
}

func (scheduler *Scheduler) Execute(
	ctx context.Context, scheduled ScheduledJob,
) error {
	var lastErr error
	for attempt := 1; attempt <= scheduler.retry.MaximumAttempts; attempt++ {
		runID := int64(0)
		if scheduler.store != nil {
			var err error
			runID, err = scheduler.store.StartRun(
				ctx, scheduled.Job.Name, scheduled.At, attempt,
			)
			if err != nil {
				return err
			}
		}
		lastErr = scheduled.Job.Run(ctx)
		status := StatusSucceeded
		message := ""
		if lastErr != nil {
			status = StatusRetrying
			message = lastErr.Error()
			if attempt == scheduler.retry.MaximumAttempts {
				status = StatusFailed
			}
		}
		if scheduler.store != nil {
			if err := scheduler.store.FinishRun(ctx, runID, status, message); err != nil {
				return err
			}
		}
		if lastErr == nil {
			return nil
		}
		if attempt < scheduler.retry.MaximumAttempts {
			delay := scheduler.retry.InitialDelay << (attempt - 1)
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return context.Cause(ctx)
			case <-timer.C:
			}
		}
	}
	return lastErr
}
