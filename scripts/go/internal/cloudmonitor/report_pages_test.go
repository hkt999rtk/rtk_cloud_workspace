package cloudmonitor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestCertificateDisplayFoldRetainsRisksAndOriginalJSON(t *testing.T) {
	at := time.Now().UTC()
	var rows []Result
	for i := 0; i < 1259; i++ {
		expiry := at.Add(time.Duration(i+31) * 24 * time.Hour)
		rows = append(rows, Result{Service: "pki", CheckID: fmt.Sprintf("certificate-inventory/id%d", i), Required: i%2 == 0, Status: Pass, ObservedAt: at, ExpiresAt: &expiry, Reason: "normal"})
	}
	for i, status := range []Status{Fail, Warn, Unknown, NotApplicable, NotRun} {
		rows = append(rows, Result{Service: "pki", CheckID: fmt.Sprintf("certificate-inventory/risk%d", i), Status: status, Reason: "full risk evidence", Fingerprint: "original-fingerprint"})
	}
	rows = append(rows, Result{Service: "api", CheckID: "tls/live", Status: Pass, Reason: "retain live check"})
	report := Report{Groups: []ReportGroup{{Title: "credentials", Results: rows}}}
	before, _ := json.Marshal(report)
	pages := resultPages(report.Groups[0].Results)
	var shown []Result
	for _, page := range pages {
		shown = append(shown, page...)
	}
	after, _ := json.Marshal(report)
	if !bytes.Equal(before, after) || len(report.Groups[0].Results) != 1265 {
		t.Fatal("display fold changed report JSON")
	}
	if len(shown) != 7 || shown[0].Value == nil || *shown[0].Value != 1259 || !strings.Contains(shown[0].Reason, "1259 筆 PASS") || !strings.Contains(shown[0].Reason, "必要 630 筆") {
		t.Fatalf("bad display summary %+v", shown)
	}
	for i, status := range []Status{Fail, Warn, Unknown, NotApplicable, NotRun} {
		row := shown[i+1]
		if row.Status != status || row.Reason != "full risk evidence" || row.Fingerprint != "original-fingerprint" {
			t.Fatal("risk evidence altered")
		}
	}
	if shown[6].CheckID != "tls/live" {
		t.Fatal("noninventory PASS folded")
	}
	if shown[0].ExpiresAt == nil || !shown[0].ExpiresAt.Equal(at.Add(31*24*time.Hour)) {
		t.Fatal("summary expiry incorrect")
	}
}

func TestSmallCertificateInventoryIsNotFolded(t *testing.T) {
	rows := make([]Result, 40)
	for i := range rows {
		rows[i] = Result{CheckID: fmt.Sprintf("certificate-inventory/id%d", i), Status: Pass}
	}
	got := foldNormalCertificateRows(rows)
	if len(got) != 40 || got[0].CheckID != rows[0].CheckID {
		t.Fatal("small fixture unexpectedly folded")
	}
}

func TestCertificateEventFoldRetainsRisksAndCompleteJSON(t *testing.T) {
	at := time.Now().UTC()
	var events []ReportEvent
	for i := 0; i < 1259; i++ {
		events = append(events, ReportEvent{Time: at.Add(time.Duration(i) * time.Second), Service: "pki", CheckID: fmt.Sprintf("certificate-inventory/id%d", i), Status: Pass, Reason: "recovered"})
	}
	for i, status := range []Status{Fail, Warn, Unknown} {
		events = append(events, ReportEvent{Time: at, Service: "pki", CheckID: fmt.Sprintf("certificate-inventory/risk%d", i), Status: status, Reason: "full event evidence"})
	}
	events = append(events, ReportEvent{Service: "api", CheckID: "http/live", Status: Pass, Reason: "ready"})
	report := Report{Events: events}
	before, _ := json.Marshal(report)
	var shown []ReportEvent
	for _, p := range eventPages(report.Events) {
		shown = append(shown, p...)
	}
	after, _ := json.Marshal(report)
	if !bytes.Equal(before, after) || len(report.Events) != 1263 {
		t.Fatal("event rendering changed complete JSON")
	}
	if len(shown) != 5 || !strings.Contains(shown[0].Reason, "1259 筆 PASS") || !shown[0].Time.Equal(at) {
		t.Fatal("PASS event summary incorrect")
	}
	for i, status := range []Status{Fail, Warn, Unknown} {
		if shown[i+1].Status != status || shown[i+1].Reason != "full event evidence" {
			t.Fatal("risk event dropped or changed")
		}
	}
	if shown[4].CheckID != "http/live" {
		t.Fatal("other PASS event folded")
	}
}
