package cloudmonitor

import (
	"fmt"
	"strings"
)

// Tables are emitted as short independent blocks. Chromium can move a complete
// block to the next page without fragmenting table cells; each block has a header.
func servicePages(rows []ServiceReport) [][]ServiceReport {
	var pages [][]ServiceReport
	start, height := 0, 0
	for i, r := range rows {
		h := 16*max(2, reportTextLines(r.Name, 54)) + 16
		if i > start && (i-start >= 16 || height+h > 490) {
			pages = append(pages, rows[start:i])
			start = i
			height = 0
		}
		height += h
	}
	if start < len(rows) {
		pages = append(pages, rows[start:])
	}
	return pages
}

func resultPages(rows []Result) [][]Result {
	rows = foldNormalCertificateRows(rows)
	var pages [][]Result
	start, height := 0, 0
	for i, r := range rows {
		lines := max(reportTextLines(r.Service, 36)+reportTextLines(r.CheckID, 36)+2, 1+reportTextLines(r.Threshold, 34), 3+reportTextLines(r.Fingerprint, 32), reportTextLines(r.Reason, 50)+reportTextLines(r.Recommendation, 50)+reportTextLines(r.Source, 50)+1)
		if r.ObservedStatus != "" {
			lines = max(lines, 5)
		}
		h := 16*lines + 20
		if i > start && (i-start >= 5 || height+h > 490) {
			pages = append(pages, rows[start:i])
			start = i
			height = 0
		}
		height += h
	}
	if start < len(rows) {
		pages = append(pages, rows[start:])
	}
	return pages
}

// Fold only the display copy. JSON reports, health counts and raw history keep
// every original inventory entry. Risk records always retain their full rows.
func foldNormalCertificateRows(rows []Result) []Result {
	count, required, total := 0, 0, 0
	var summary Result
	for _, r := range rows {
		if !strings.HasPrefix(r.CheckID, "certificate-inventory/") {
			continue
		}
		total++
		if r.Status != Pass {
			continue
		}
		count++
		if r.Required {
			required++
		}
		if count == 1 {
			summary.Service = r.Service
			summary.ObservedAt = r.ObservedAt
		}
		if r.ObservedAt.Before(summary.ObservedAt) {
			summary.ObservedAt = r.ObservedAt
		}
		if r.ExpiresAt != nil && (summary.ExpiresAt == nil || r.ExpiresAt.Before(*summary.ExpiresAt)) {
			expiry := *r.ExpiresAt
			summary.ExpiresAt = &expiry
		}
	}
	if count <= 40 {
		return rows
	}
	value := float64(count)
	summary.CheckID = "certificate-inventory/正常盤點摘要"
	summary.Layer = "credential"
	summary.Required = required > 0
	summary.Status = Pass
	summary.Value = &value
	summary.Unit = "筆盤點紀錄"
	summary.Source = "同名 JSON 報告及 JSONL 歷史保留全部原始紀錄"
	summary.Reason = fmt.Sprintf("共 %d 筆憑證盤點紀錄；%d 筆 PASS 已折疊（必要 %d 筆、非必要 %d 筆）。其餘 %d 筆逐項保留。效期欄為正常項目的最早到期日；各筆身分、來源與指紋請見完整 JSON。", total, count, required, count-required, total-count)
	out := make([]Result, 0, len(rows)-count+1)
	inserted := false
	for _, r := range rows {
		if r.Status == Pass && strings.HasPrefix(r.CheckID, "certificate-inventory/") {
			if !inserted {
				out = append(out, summary)
				inserted = true
			}
			continue
		}
		out = append(out, r)
	}
	return out
}

func eventPages(rows []ReportEvent) [][]ReportEvent {
	rows = foldNormalCertificateEvents(rows)
	var pages [][]ReportEvent
	start, height := 0, 0
	for i, r := range rows {
		lines := max(2, reportTextLines(r.Service+" / "+r.CheckID, 42), reportTextLines(r.Reason, 94))
		h := 16*lines + 16
		if i > start && (i-start >= 8 || height+h > 490) {
			pages = append(pages, rows[start:i])
			start = i
			height = 0
		}
		height += h
	}
	if start < len(rows) {
		pages = append(pages, rows[start:])
	}
	return pages
}

func foldNormalCertificateEvents(rows []ReportEvent) []ReportEvent {
	count, total := 0, 0
	var summary ReportEvent
	for _, r := range rows {
		if !strings.HasPrefix(r.CheckID, "certificate-inventory/") {
			continue
		}
		total++
		if r.Status != Pass {
			continue
		}
		count++
		if count == 1 {
			summary.Service = r.Service
			summary.Time = r.Time
		}
		if r.Time.Before(summary.Time) {
			summary.Time = r.Time
		}
	}
	if count <= 40 {
		return rows
	}
	last := summary.Time
	for _, r := range rows {
		if r.Status == Pass && strings.HasPrefix(r.CheckID, "certificate-inventory/") && r.Time.After(last) {
			last = r.Time
		}
	}
	summary.CheckID = "certificate-inventory/正常盤點事件摘要"
	summary.Status = Pass
	summary.Reason = fmt.Sprintf("已折疊 %d 筆 PASS 憑證盤點事件（UTC %s 至 %s）；其餘 %d 筆憑證事件及所有其他事件逐項保留。完整時序請見同名 JSON 報告。", count, summary.Time.UTC().Format("2006-01-02T15:04:05Z"), last.UTC().Format("2006-01-02T15:04:05Z"), total-count)
	out := make([]ReportEvent, 0, len(rows)-count+1)
	inserted := false
	for _, r := range rows {
		if r.Status == Pass && strings.HasPrefix(r.CheckID, "certificate-inventory/") {
			if !inserted {
				out = append(out, summary)
				inserted = true
			}
			continue
		}
		out = append(out, r)
	}
	return out
}

func chartPages(rows []ReportChart) [][]ReportChart {
	var pages [][]ReportChart
	for len(rows) > 0 {
		n := min(4, len(rows))
		pages = append(pages, rows[:n])
		rows = rows[n:]
	}
	return pages
}

func reportTextLines(s string, width int) int {
	if s == "" {
		return 0
	}
	lines, units := 1, 0
	for _, r := range s {
		if r == '\n' {
			lines++
			units = 0
			continue
		}
		weight := 1
		if r > 127 {
			weight = 2
		}
		units += weight
		if units > width {
			lines++
			units = weight
		}
	}
	return lines
}
