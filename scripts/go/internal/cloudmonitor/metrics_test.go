package cloudmonitor

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestPrometheusFreshnessThresholdMissingAndMean(t *testing.T) {
	now := time.Now().UTC()
	warn := 80.0
	for _, tc := range []struct {
		name  string
		age   int
		value string
		want  Status
	}{{"fresh", 0, "60", Pass}, {"high", 0, "90", Warn}, {"stale", 300, "60", Unknown}, {"nan", 0, "NaN", Unknown}} {
		t.Run(tc.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/query" || r.URL.Query().Get("query") != "usage" {
					t.Error("query shape")
				}
				fmt.Fprintf(w, `{"status":"success","data":{"resultType":"vector","result":[{"metric":{"instance":"safe"},"value":[%d,%q]}]}}`, now.Add(-time.Duration(tc.age)*time.Second).Unix(), tc.value)
			}))
			defer s.Close()
			c := CollectMetrics(context.Background(), Config{Metrics: []MetricCheck{{Name: "usage", Service: "db", URL: s.URL, Query: "usage", Required: true, WarnAbove: &warn}}}, Runtime{}, now)
			if c.Results[0].Status != tc.want {
				t.Fatalf("%s: %s", c.Results[0].Status, c.Results[0].Reason)
			}
		})
	}
}
func TestPrometheusNoLimitMeansUnvalidated(t *testing.T) {
	now := time.Now()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"status":"success","data":{"resultType":"scalar","result":[%d,"500"]}}`, now.Unix())
	}))
	defer s.Close()
	c := CollectMetrics(context.Background(), Config{Metrics: []MetricCheck{{Name: "tps", Service: "db", URL: s.URL, Query: "transactions", Required: true}}}, Runtime{}, now)
	if c.Results[0].Status != Unknown || len(c.Samples) != 1 {
		t.Fatal("throughput without target keeps observation but unknown acceptance")
	}
}
