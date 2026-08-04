package postgres

import (
	"testing"
	"time"
)

func TestLatestLearningTradingDate(t *testing.T) {
	t.Parallel()

	july29 := time.Date(2026, time.July, 29, 0, 0, 0, 0, time.UTC)
	july30 := time.Date(2026, time.July, 30, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name       string
		candidates []*time.Time
		want       time.Time
		wantOK     bool
	}{
		{
			name:   "no stored market data",
			wantOK: false,
		},
		{
			name:       "daily bars only",
			candidates: []*time.Time{&july29},
			want:       july29,
			wantOK:     true,
		},
		{
			name: "intraday market data is newer than daily bars",
			candidates: []*time.Time{
				&july29,
				&july30,
				&july30,
				nil,
			},
			want:   july30,
			wantOK: true,
		},
		{
			name: "scanner signal can establish current trading date",
			candidates: []*time.Time{
				nil,
				nil,
				nil,
				&july30,
			},
			want:   july30,
			wantOK: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, ok := latestLearningTradingDate(tt.candidates...)
			if ok != tt.wantOK {
				t.Fatalf("ok = %t, want %t", ok, tt.wantOK)
			}
			if !got.Equal(tt.want) {
				t.Fatalf("date = %s, want %s", got, tt.want)
			}
		})
	}
}
