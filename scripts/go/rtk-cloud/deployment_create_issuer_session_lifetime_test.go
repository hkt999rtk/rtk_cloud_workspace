package main

import (
	"testing"
	"time"
)

func TestIssuerEnrollmentCertificateAllowsExpiredSessionBeforeCertificate(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	past, future := now.Add(-time.Hour), now.Add(time.Hour)
	for _, tc := range []struct {
		name                   string
		start, end, sessionEnd time.Time
		expired, want          bool
	}{
		{"expired-session-valid-cert", past, future, past, true, true},
		{"expired-session-expired-cert", past.Add(-time.Hour), past, past, true, true},
		{"active-session-valid-cert", past, future, future, false, true},
		{"active-session-expired-cert", past.Add(-time.Hour), past, past, false, false},
		{"session-outlives-cert", past, now, future, true, false},
		{"not-yet-valid-cert", future, future.Add(time.Hour), future, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := issuerEnrollmentCertificateTimeValid(tc.start, tc.end, tc.sessionEnd, now, tc.expired); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
