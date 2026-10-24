package main

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func overlayRedisExtensionAuthority() *createManagedLateNormalTailExtensionAuthority {
	now := time.Now().UTC()
	start := now.Add(-2 * time.Hour)
	end := start.Add(360 * time.Minute)
	a := &createManagedLateNormalTailExtensionAuthority{}
	v := reflect.ValueOf(a).Elem()
	for i := 0; i < v.NumField(); i++ {
		if v.Field(i).Kind() == reflect.String {
			v.Field(i).SetString(strings.Repeat("a", 64))
		}
	}
	a.Schema = managedLateNormalTailExtensionSchema
	a.OriginalStartedAt = start
	a.BaseDeadline = start.Add(180 * time.Minute)
	a.ExtendedDeadline = end
	a.FirstCallerDeadline = end
	a.AdmittedAt = now
	a.Deadline = end
	a.TotalLimit = 360 * time.Minute
	return a
}
func TestManagedLateRedisOverlayExtensionRemainsTypedAndLeaseFiveMinutes(t *testing.T) {
	a := overlayRedisExtensionAuthority()
	f := newRedisNativeFixture()
	if !managedLateNormalTailExtensionPauseMatches(a, a.Deadline) {
		t.Fatal("synthetic typed fixture invalid")
	}
	if _, err := beginManagedLateRedisPause(t.Context(), f.run, a.Deadline); err == nil || f.pauseCalls != 0 {
		t.Fatal("default90 inherited360")
	}
	p, err := beginManagedLateRedisPauseWithExtension(t.Context(), f.run, a.Deadline, a)
	if err != nil {
		t.Fatal(err)
	}
	if p.Until.After(time.Now().Add(5*time.Minute)) || !p.Deadline.Equal(a.OriginalStartedAt.Add(360*time.Minute)) || managedLateOverallLimit != 90*time.Minute || managedLateNormalTailSupplementLimit != 180*time.Minute {
		t.Fatal("extension changed anchor, old limits, or short lease")
	}
	calls := f.pauseCalls
	p.extension.TotalLimit++
	if p.Check(time.Now().UTC()) == nil || p.Renew(t.Context()) == nil || f.pauseCalls != calls {
		t.Fatal("mutated local authority permitted native renewal")
	}
	if err := p.Unpause(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"nil", "schema", "anchor", "base-pin", "deadline", "mixed"} {
		t.Run(mode, func(t *testing.T) {
			a := overlayRedisExtensionAuthority()
			deadline := a.Deadline
			f := newRedisNativeFixture()
			switch mode {
			case "nil":
				a = nil
			case "schema":
				a.Schema = "foreign"
			case "anchor":
				a.OriginalStartedAt = a.OriginalStartedAt.Add(time.Minute)
			case "base-pin":
				a.BaseAuthorityRawSHA = ""
			case "deadline":
				deadline = deadline.Add(time.Minute)
			}
			if mode == "mixed" {
				p := &managedLateRedisPause{Deadline: deadline, run: f.run, extension: a, supplement: redisNativeSupplementFixture()}
				if p.Renew(t.Context()) == nil {
					t.Fatal("mixed authorities accepted")
				}
				return
			}
			if _, err := beginManagedLateRedisPauseWithExtension(t.Context(), f.run, deadline, a); err == nil || f.pauseCalls != 0 || f.unpauseCalls != 0 {
				t.Fatal("invalid extension reached a native command")
			}
		})
	}
}
