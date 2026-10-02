package cloudmonitor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func CollectMetrics(ctx context.Context, cfg Config, rt Runtime, now time.Time) Collection {
	var tasks []func(context.Context) Collection
	for _, m := range cfg.Metrics {
		m := m
		tasks = append(tasks, func(c context.Context) Collection {
			single := cfg
			single.Metrics = []MetricCheck{m}
			return collectMetricChecks(c, single, rt, now)
		})
	}
	return parallelCollections(ctx, cfg, tasks)
}
func collectMetricChecks(ctx context.Context, cfg Config, rt Runtime, now time.Time) Collection {
	var out Collection
	for _, m := range cfg.Metrics {
		r := result(m.Service, "metric/"+m.Name, "performance", m.Required, now)
		r.Source = "prometheus"
		base, err := url.Parse(m.URL)
		if err != nil {
			r.Reason = "Prometheus URL 無效"
			out.Results = append(out.Results, r)
			continue
		}
		base.Path = strings.TrimRight(base.Path, "/") + "/api/v1/query"
		q := base.Query()
		q.Set("query", m.Query)
		q.Set("time", strconv.FormatFloat(float64(now.UnixNano())/1e9, 'f', 3, 64))
		base.RawQuery = q.Encode()
		client, err := HTTPClient(m.CAFile, "", "", rt, timeout(cfg))
		if err != nil {
			r.Reason = "Prometheus client 配置錯誤"
			out.Results = append(out.Results, r)
			continue
		}
		c, cancel := context.WithTimeout(ctx, timeout(cfg))
		req, err := http.NewRequestWithContext(c, http.MethodGet, base.String(), nil)
		if err != nil {
			cancel()
			r.Reason = "Prometheus request 無效"
			out.Results = append(out.Results, r)
			continue
		}
		if m.TokenFile != "" {
			token, e := ReadCredential(rt, m.TokenFile)
			if e != nil {
				cancel()
				r.Reason = "Prometheus token 無法讀取"
				out.Results = append(out.Results, r)
				continue
			}
			req.Header.Set("Authorization", "Bearer "+string(token))
		}
		resp, err := client.Do(req)
		if err != nil {
			cancel()
			r.Reason = "Prometheus 查詢無法連線或逾時"
			out.Results = append(out.Results, r)
			continue
		}
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20+1))
		resp.Body.Close()
		cancel()
		if err != nil || len(raw) > 4<<20 || resp.StatusCode != 200 {
			r.Reason = "Prometheus 查詢回應失敗、權限不足或超限"
			out.Results = append(out.Results, r)
			continue
		}
		var response struct {
			Status string `json:"status"`
			Data   struct {
				Type   string          `json:"resultType"`
				Result json.RawMessage `json:"result"`
			} `json:"data"`
		}
		if json.Unmarshal(raw, &response) != nil || response.Status != "success" {
			r.Reason = "Prometheus 查詢未成功或回應無法解析"
			out.Results = append(out.Results, r)
			continue
		}
		var points []struct {
			Metric map[string]string `json:"metric"`
			Value  []json.RawMessage `json:"value"`
		}
		if response.Data.Type == "scalar" {
			var point []json.RawMessage
			if json.Unmarshal(response.Data.Result, &point) == nil {
				points = append(points, struct {
					Metric map[string]string `json:"metric"`
					Value  []json.RawMessage `json:"value"`
				}{Value: point})
			}
		} else if response.Data.Type == "vector" {
			json.Unmarshal(response.Data.Result, &points)
		}
		maxAge := m.MaxAgeSeconds
		if maxAge == 0 {
			maxAge = 120
		}
		r.Status = Pass
		r.Reason = countReason(len(points))
		valid := 0
		var maxValue float64
		for _, p := range points {
			if len(p.Value) != 2 {
				continue
			}
			var ts float64
			var text string
			if json.Unmarshal(p.Value[0], &ts) != nil || json.Unmarshal(p.Value[1], &text) != nil {
				continue
			}
			value, e := strconv.ParseFloat(text, 64)
			if e != nil || math.IsNaN(value) || math.IsInf(value, 0) {
				continue
			}
			when := time.Unix(0, int64(ts*1e9))
			if when.After(now.Add(5*time.Second)) || now.Sub(when) > time.Duration(maxAge)*time.Second {
				continue
			}
			labels, _ := json.Marshal(p.Metric)
			hash := sha256.Sum256(labels)
			out.Samples = append(out.Samples, Sample{Service: m.Service, Name: m.Name, Value: value, Unit: m.Unit, Kind: "gauge", ObservedAt: when, Identity: hex.EncodeToString(hash[:8])})
			if valid == 0 || value > maxValue {
				maxValue = value
			}
			valid++
		}
		if len(points) == 0 || valid != len(points) {
			r.Status = Unknown
			r.Reason = "Prometheus 資料缺少、過期或數值無效"
		} else {
			r.Value = numeric(maxValue)
			r.Unit = m.Unit
			if m.FailAbove != nil && maxValue >= *m.FailAbove {
				r.Status = Fail
				r.Reason = "指標超過設定失敗門檻"
			} else if m.WarnAbove != nil && maxValue >= *m.WarnAbove {
				r.Status = Warn
				r.Reason = "指標超過設定警告門檻"
			}
			if m.Baseline > 0 {
				r.Threshold = "已驗證基準：" + m.BaselineEvidence
				if maxValue >= m.Baseline && r.Status != Fail {
					r.Status = Warn
					r.Reason = "目前數值已達適用的已驗證基準"
				}
			} else if m.WarnAbove == nil && m.FailAbove == nil {
				r.Status = Unknown
				r.Reason = "已有觀測值；尚無判定目標或適用的已驗證基準"
			}
		}
		out.Results = append(out.Results, r)
	}
	return out
}
