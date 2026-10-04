package frankfurter

import (
	"net/http"
	"testing"
	"time"
)

func TestRetryAfter(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		value string
		want  time.Duration
	}{
		{"", 0}, {"0", 0}, {"15", 15 * time.Second}, {" 2 ", 2 * time.Second}, {"-1", 0}, {"+1", 0}, {"1.5", 0}, {"garbage", 0}, {"18446744073709551616", 0}, {"9223372037", 0},
		{now.Add(time.Minute).Format(http.TimeFormat), time.Minute}, {now.Add(-time.Minute).Format(http.TimeFormat), 0},
	} {
		if got := parseRetryAfter(tc.value, now); got != tc.want {
			t.Errorf("%q: got %v want %v", tc.value, got, tc.want)
		}
	}
}
