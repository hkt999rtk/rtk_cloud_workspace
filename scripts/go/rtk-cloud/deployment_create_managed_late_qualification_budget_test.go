package main

import (
	"context"
	"testing"
	"time"
)

func TestManagedLateQualificationWindowKeepsNativePreparationBound(t *testing.T) {
	parent, cancel := context.WithTimeout(context.Background(), managedLateOverallLimit)
	defer cancel()
	started := time.Now().Add(-31 * time.Minute)
	qualified, stop, err := managedLatePreparationContextForOperation(parent, started, false)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	deadline, _ := qualified.Deadline()
	if !deadline.Equal(started.Add(45 * time.Minute)) {
		t.Fatal("qualification reset original command start")
	}
	if _, _, err := managedLatePreparationContextForOperation(parent, started, true); err == nil {
		t.Fatal("qualification extended native preparation")
	}
	nativeStart := time.Now().Add(-time.Minute)
	native, nstop, err := managedLatePreparationContextForOperation(parent, nativeStart, true)
	if err != nil {
		t.Fatal(err)
	}
	defer nstop()
	nd, _ := native.Deadline()
	if !nd.Equal(nativeStart.Add(30 * time.Minute)) {
		t.Fatal("native preparation limit changed")
	}
	early, ecancel := context.WithTimeout(parent, time.Minute)
	defer ecancel()
	clamped, cstop, err := managedLatePreparationContextForOperation(early, time.Now(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer cstop()
	ed, _ := early.Deadline()
	cd, _ := clamped.Deadline()
	if !cd.Equal(ed) {
		t.Fatal("qualification extended original parent deadline")
	}
}

func TestManagedLateQualificationRejectsInvalidOrExhaustedCaller(t *testing.T) {
	bounded, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	canceled, stop := context.WithCancel(bounded)
	stop()
	for _, tc := range []struct {
		name  string
		ctx   context.Context
		start time.Time
	}{
		{"nil", nil, time.Now()}, {"unbounded", context.Background(), time.Now()}, {"canceled", canceled, time.Now()}, {"zero", bounded, time.Time{}}, {"future", bounded, time.Now().Add(time.Minute)}, {"expired", bounded, time.Now().Add(-45 * time.Minute)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := managedLatePreparationContextForOperation(tc.ctx, tc.start, false); err == nil {
				t.Fatal("invalid qualification clock accepted")
			}
		})
	}
}
