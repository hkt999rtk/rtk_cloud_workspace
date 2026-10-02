package cloudmonitor

import (
	"context"
	"time"
)

// CollectSynthetic runs only when the caller explicitly selects functional
// probes. Missing probe classes remain visible in coverage.
func CollectSynthetic(ctx context.Context, cfg Config, rt Runtime, runner CommandRunner, now time.Time) Collection {
	var out Collection
	for _, p := range []struct {
		name       string
		configured int
	}{{"auth", len(cfg.Synthetic.Auth)}, {"mqtt", len(cfg.Synthetic.MQTT)}, {"turn", len(cfg.Synthetic.TURN)}} {
		r := result(p.name, "synthetic/"+p.name, "functional", true, now)
		if p.configured == 0 {
			r.Status = NotRun
			r.Reason = "專用功能測試尚未配置"
			out.Results = append(out.Results, r)
		}
	}
	if len(cfg.Synthetic.Auth)+len(cfg.Synthetic.MQTT)+len(cfg.Synthetic.TURN) == 0 {
		return out
	}
	groupCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	lock, err := acquireSyntheticLock(rt)
	if err != nil {
		r := result("synthetic", "synthetic/execution", "functional", true, now)
		r.Reason = "專用監看 state 無法取得或已有另一組功能測試"
		out.Results = append(out.Results, r)
		return out
	}
	defer lock.Close()
	appendProbe := func(r Result) {
		out.Results = append(out.Results, r)
		if r.Status == Pass {
			out.Samples = append(out.Samples, Sample{Service: r.Service, Name: "synthetic_probe_latency_ms", Value: float64(r.DurationMS), Unit: "ms", Kind: "gauge", ObservedAt: now, Identity: r.CheckID})
		}
	}
	for _, probe := range cfg.Synthetic.Auth {
		probeCtx, end := context.WithTimeout(groupCtx, 30*time.Second)
		appendProbe(probeSyntheticAuth(probeCtx, cfg, rt, probe, now))
		end()
	}
	for _, probe := range cfg.Synthetic.MQTT {
		probeCtx, end := context.WithTimeout(groupCtx, 30*time.Second)
		r, cleanup := probeSyntheticMQTT(probeCtx, cfg, rt, probe, now)
		appendProbe(r)
		out.Results = append(out.Results, cleanup)
		end()
	}
	for _, probe := range cfg.Synthetic.TURN {
		probeCtx, end := context.WithTimeout(groupCtx, 30*time.Second)
		r, cleanup := probeSyntheticTURN(probeCtx, cfg, rt, probe, now)
		appendProbe(r)
		out.Results = append(out.Results, cleanup)
		end()
	}
	return out
}
