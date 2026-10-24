package main

import (
	"context"
	"testing"
	"time"
)

func TestManagedLateNormalTailSupplementCallerLimitRetainsOrdinary90(t *testing.T) {
	for _, test := range []struct {
		name                      string
		duration                  time.Duration
		afterApplication, allowed bool
	}{
		{"ordinary90", 90 * time.Minute, false, true},
		{"ordinaryOver90", 90*time.Minute + time.Nanosecond, false, false},
		{"normal180", 180 * time.Minute, true, true},
		{"normalOver360", 360*time.Minute + time.Nanosecond, true, false},
		{"zero", 0, true, false},
		{"negative", -time.Second, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if managedLateNormalTailCallerTimeoutAllowed(test.duration, test.afterApplication) != test.allowed {
				t.Fatal("caller limit changed outside the explicit supplemental profile")
			}
		})
	}
}

func TestManagedLateNormalTailSupplementLongCallerRequiresAuthenticatedSource(t *testing.T) {
	for _, ctx := range []context.Context{nil, context.Background()} {
		if _, cancel, err := managedLateNormalTailSupplementCallerContext(ctx, secretStore{}, nil, 180*time.Minute, true); err == nil {
			cancel()
			t.Fatal("long caller accepted without exact source and new approval")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, close, err := managedLateNormalTailSupplementCallerContext(ctx, secretStore{}, nil, 180*time.Minute, true); err == nil {
		close()
		t.Fatal("canceled long caller accepted")
	}
	ordinary := context.Background()
	got, close, err := managedLateNormalTailSupplementCallerContext(ordinary, secretStore{}, nil, 90*time.Minute, false)
	defer close()
	if err != nil || got != ordinary {
		t.Fatal("ordinary caller acquired supplemental prerequisites")
	}
}
