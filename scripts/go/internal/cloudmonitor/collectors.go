package cloudmonitor

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

func result(service, id, layer string, required bool, now time.Time) Result {
	return Result{Service: service, CheckID: id, Layer: layer, Required: required, Status: Unknown, ObservedAt: now}
}
func numeric(v float64) *float64 { return &v }
func timeout(cfg Config) time.Duration {
	if cfg.TimeoutSeconds > 0 {
		return time.Duration(cfg.TimeoutSeconds) * time.Second
	}
	return 5 * time.Second
}
func kube(ctx context.Context, rt Runtime, runner CommandRunner, args ...string) ([]byte, int, error) {
	return runner.Run(ctx, "kubectl", append([]string{"--kubeconfig", rt.Kubeconfig, "--request-timeout=5s"}, args...), nil, nil)
}

// CollectReadOnly never invokes synthetic probes, sweep, repair or credential rotation.
type collectorBudgetKey struct{}

func collectorBudget(ctx context.Context, cfg Config) (context.Context, chan struct{}) {
	if sem, ok := ctx.Value(collectorBudgetKey{}).(chan struct{}); ok {
		return ctx, sem
	}
	n := cfg.Concurrency
	if n < 1 {
		n = 8
	}
	sem := make(chan struct{}, n)
	return context.WithValue(ctx, collectorBudgetKey{}, sem), sem
}
func parallelCollections(ctx context.Context, cfg Config, tasks []func(context.Context) Collection) Collection {
	ctx, sem := collectorBudget(ctx, cfg)
	parts := make([]Collection, len(tasks))
	var wg sync.WaitGroup
	for i, fn := range tasks {
		wg.Add(1)
		go func(i int, fn func(context.Context) Collection) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
				parts[i] = fn(ctx)
			case <-ctx.Done():
			}
		}(i, fn)
	}
	wg.Wait()
	return Merge(parts...)
}
func CollectReadOnly(ctx context.Context, cfg Config, inv Inventory, rt Runtime, runner CommandRunner, now time.Time) Collection {
	ctx, _ = collectorBudget(ctx, cfg)
	tasks := []func() Collection{func() Collection { return CollectKubernetes(ctx, cfg, inv, rt, runner, now) }, func() Collection { return CollectHTTP(ctx, cfg, inv, rt, now) }, func() Collection { return CollectTLS(ctx, cfg, rt, now) }, func() Collection { return CollectCertificates(ctx, cfg, rt, runner, now) }, func() Collection { return CollectTokens(ctx, cfg, rt, now) }, func() Collection { return CollectDatabases(ctx, cfg, rt, runner, now) }, func() Collection { return CollectMetrics(ctx, cfg, rt, now) }}
	var wg sync.WaitGroup
	parts := make([]Collection, len(tasks))
	for i, fn := range tasks {
		wg.Add(1)
		go func(i int, fn func() Collection) { defer wg.Done(); parts[i] = fn() }(i, fn)
	}
	wg.Wait()
	out := Merge(parts...)
	sort.SliceStable(out.Results, func(i, j int) bool {
		a, b := out.Results[i], out.Results[j]
		return a.Service+"/"+a.CheckID < b.Service+"/"+b.CheckID
	})
	return out
}

type kubeObject struct {
	Kind     string `json:"kind"`
	Metadata struct {
		Name            string `json:"name"`
		Namespace       string `json:"namespace"`
		Generation      int64  `json:"generation"`
		UID             string `json:"uid"`
		OwnerReferences []struct {
			Kind string `json:"kind"`
			Name string `json:"name"`
		} `json:"ownerReferences"`
	} `json:"metadata"`
	Spec struct {
		Replicas *int `json:"replicas"`
		Template struct {
			Spec struct {
				Containers []struct {
					Name string `json:"name"`
					Env  []struct {
						Name  string `json:"name"`
						Value string `json:"value"`
					} `json:"env"`
				} `json:"containers"`
			} `json:"spec"`
		} `json:"template"`
	} `json:"spec"`
	Status struct {
		ObservedGeneration     int64  `json:"observedGeneration"`
		Replicas               int    `json:"replicas"`
		ReadyReplicas          int    `json:"readyReplicas"`
		UpdatedReplicas        int    `json:"updatedReplicas"`
		AvailableReplicas      int    `json:"availableReplicas"`
		DesiredNumberScheduled int    `json:"desiredNumberScheduled"`
		NumberReady            int    `json:"numberReady"`
		Phase                  string `json:"phase"`
		Conditions             []struct {
			Type   string `json:"type"`
			Status string `json:"status"`
		} `json:"conditions"`
		ContainerStatuses []struct {
			Name         string `json:"name"`
			Ready        bool   `json:"ready"`
			RestartCount int    `json:"restartCount"`
			State        struct {
				Waiting *struct {
					Reason string `json:"reason"`
				} `json:"waiting"`
				Terminated *struct {
					Reason   string `json:"reason"`
					ExitCode int    `json:"exitCode"`
				} `json:"terminated"`
			} `json:"state"`
			LastState struct {
				Terminated *struct {
					Reason     string    `json:"reason"`
					FinishedAt time.Time `json:"finishedAt"`
				} `json:"terminated"`
			} `json:"lastState"`
		} `json:"containerStatuses"`
	} `json:"status"`
}

func workloadStatus(o kubeObject) (Status, string) {
	switch strings.ToLower(o.Kind) {
	case "deployment", "statefulset":
		want := 1
		if o.Spec.Replicas != nil {
			want = *o.Spec.Replicas
		}
		if want < 1 {
			return Fail, "預期服務 replicas 為零"
		}
		if o.Status.ObservedGeneration < o.Metadata.Generation {
			return Fail, "workload 尚未觀測最新 generation"
		}
		if o.Status.ReadyReplicas < want {
			return Fail, "ready replicas 少於預期"
		}
		if strings.EqualFold(o.Kind, "deployment") && (o.Status.UpdatedReplicas < want || o.Status.AvailableReplicas < want) {
			return Fail, "Deployment rollout 未完成"
		}
		return Pass, "workload replicas 與 generation 就緒"
	case "daemonset":
		if o.Status.DesiredNumberScheduled < 1 || o.Status.NumberReady < o.Status.DesiredNumberScheduled {
			return Fail, "DaemonSet 尚未全數就緒"
		}
		return Pass, "DaemonSet 全數就緒"
	case "persistentvolumeclaim", "pvc":
		if o.Status.Phase != "Bound" {
			return Fail, "PVC 尚未 Bound"
		}
		return Pass, "PVC 已 Bound；容量需另行採集"
	case "pod":
		if o.Status.Phase != "Running" {
			return Fail, "Pod 未處於 Running"
		}
		for _, c := range o.Status.ContainerStatuses {
			if !c.Ready {
				return Fail, "Pod container 未就緒"
			}
		}
		if len(o.Status.ContainerStatuses) == 0 {
			return Unknown, "Pod container 狀態缺少"
		}
		return Pass, "Pod containers 就緒"
	default:
		return Unknown, "此 target 的就緒條件未實作"
	}
}
func CollectKubernetes(ctx context.Context, cfg Config, inv Inventory, rt Runtime, runner CommandRunner, now time.Time) Collection {
	var out Collection
	namespaces := map[string]bool{}
	expected := map[string]bool{}
	for _, t := range inv.Targets {
		if t.Enabled && t.Namespace != "" && t.Kind != "external" {
			namespaces[t.Namespace] = true
		}
		if t.Namespace != "" {
			expected[t.Namespace+"/"+strings.ToLower(t.Kind)+"/"+t.Name] = true
		}
	}
	type nsData struct {
		objects map[string]kubeObject
		items   []kubeObject
		ok      bool
	}
	data := map[string]nsData{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	ctx, sem := collectorBudget(ctx, cfg)
	for ns := range namespaces {
		wg.Add(1)
		go func(ns string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}
			c, cancel := context.WithTimeout(ctx, timeout(cfg))
			defer cancel()
			raw, code, err := kube(c, rt, runner, "-n", ns, "get", "deployments,statefulsets,daemonsets,pods,pvc", "-o", "json")
			d := nsData{objects: map[string]kubeObject{}}
			var list struct {
				Items *[]kubeObject `json:"items"`
			}
			if err == nil && code == 0 && json.Unmarshal(raw, &list) == nil && list.Items != nil {
				d.ok = true
				d.items = *list.Items
				for _, o := range *list.Items {
					d.objects[strings.ToLower(o.Kind)+"/"+o.Metadata.Name] = o
				}
			}
			mu.Lock()
			data[ns] = d
			mu.Unlock()
		}(ns)
	}
	wg.Wait()
	var pkiturn *WorkloadTarget
	for _, t := range inv.Targets {
		r := result(t.Service, "k8s/"+t.ID, "liveness", t.Required, now)
		r.Source = "kubernetes"
		switch {
		case t.IntentUnknown:
			r.Reason = "環境未明確宣告此服務啟用意圖，需確認 expected inventory"
		case !t.Enabled:
			r.Status = NotApplicable
			r.Reason = t.DisabledReason
		case strings.EqualFold(t.Kind, "external"):
			r.Status = NotApplicable
			r.Reason = "外部服務不由 Kubernetes 管理；需由設定的實際端點或 TURN probe 驗證"
		case t.Namespace == "":
			r.Reason = "Kubernetes target 未設定 namespace"
		case !data[t.Namespace].ok:
			r.Reason = "無法讀取 namespace workload：連線或權限不足"
		default:
			o, ok := data[t.Namespace].objects[strings.ToLower(t.Kind)+"/"+t.Name]
			if !ok {
				r.Status = Fail
				r.Reason = "預期 workload 消失"
			} else {
				r.Status, r.Reason = workloadStatus(o)
				if t.Name == "pki-controller" {
					enabled := false
					for _, container := range o.Spec.Template.Spec.Containers {
						for _, e := range container.Env {
							if e.Name == "PKI_APP_CRL_WORKER_ENABLED" && e.Value == "true" {
								enabled = true
							}
						}
					}
					cr := result(t.Service, "pki/app-crl-worker", "readiness", true, now)
					cr.Source = "kubernetes"
					cr.Status = Fail
					cr.Reason = "App CRL worker 未明確啟用"
					if enabled {
						cr.Status = Pass
						cr.Reason = "App CRL worker 明確啟用"
					}
					out.Results = append(out.Results, cr)
				}
			}
		}
		out.Results = append(out.Results, r)
		if t.Name == "pkiturn" && t.Enabled && !t.IntentUnknown {
			copy := t
			pkiturn = &copy
		}
	}
	for ns, d := range data {
		r := result(ns, "k8s/drift", "coverage", true, now)
		r.Source = "kubernetes"
		if !d.ok {
			r.Reason = "無法比對 workload inventory 與 Pod 狀態"
			out.Results = append(out.Results, r)
			continue
		}
		r.Status = Pass
		r.Reason = "未發現 inventory 外的 workload"
		for _, o := range d.items {
			kind := strings.ToLower(o.Kind)
			if kind != "pod" && kind != "persistentvolumeclaim" && !expected[ns+"/"+kind+"/"+o.Metadata.Name] {
				r.Status = Warn
				r.Reason = "發現 inventory 未涵蓋的 workload"
			}
			if kind == "persistentvolumeclaim" {
				pr := result(ns, "k8s/pvc/"+o.Metadata.Name, "readiness", true, now)
				pr.Status, pr.Reason = workloadStatus(o)
				pr.Source = "kubernetes"
				out.Results = append(out.Results, pr)
			}
			if kind == "pod" {
				if o.Status.Phase == "Succeeded" {
					continue
				}
				pr := result(ns, "k8s/pod/"+o.Metadata.Name, "liveness", true, now)
				pr.Status, pr.Reason = workloadStatus(o)
				pr.Source = "kubernetes"
				for _, cs := range o.Status.ContainerStatuses {
					out.Samples = append(out.Samples, Sample{Service: ns, Name: "container_restarts", Value: float64(cs.RestartCount), Kind: "counter", Unit: "restarts", ObservedAt: now, Identity: o.Metadata.UID + "/" + cs.Name})
					if cs.LastState.Terminated != nil && cs.LastState.Terminated.Reason == "OOMKilled" && (cs.LastState.Terminated.FinishedAt.IsZero() || now.Sub(cs.LastState.Terminated.FinishedAt) < 10*time.Minute) {
						pr.Status = Warn
						pr.Reason = "container 曾被 OOMKilled"
					}
					if cs.State.Waiting != nil {
						pr.Status = Fail
						pr.Reason = "container 處於等待／重啟狀態"
					}
				}
				out.Results = append(out.Results, pr)
			}
		}
		out.Results = append(out.Results, r)
	}
	if pkiturn != nil {
		out = Merge(out, collectTurnPKI(ctx, cfg, rt, runner, *pkiturn, now))
	}
	return out
}
func collectTurnPKI(ctx context.Context, cfg Config, rt Runtime, runner CommandRunner, t WorkloadTarget, now time.Time) Collection {
	ctx, sem := collectorBudget(ctx, cfg)
	select {
	case sem <- struct{}{}:
		defer func() { <-sem }()
	case <-ctx.Done():
		return sourceGap(t.Service, "pki/read-state", "PKI 狀態採集已取消", true, now)
	}
	var out Collection
	run := func(args ...string) ([]byte, int, error) {
		c, cancel := context.WithTimeout(ctx, timeout(cfg))
		defer cancel()
		return kube(c, rt, runner, append([]string{"-n", t.Namespace}, args...)...)
	}
	r := result(t.Service, "pki/recent-sweep", "readiness", true, now)
	raw, code, err := run("exec", "deployment/pkiturn", "-c", "pkiturn", "--", "/app/pkiturn", "health")
	healthNow := time.Now().UTC()
	if now.After(healthNow) {
		healthNow = now
	}
	r.ObservedAt = healthNow
	if err != nil || code != 0 {
		r.Reason = "無法讀取 pkiturn health"
	} else {
		var h struct {
			Running       bool      `json:"running"`
			LastSucceeded bool      `json:"last_succeeded"`
			Started       time.Time `json:"started_at"`
			Finished      time.Time `json:"finished_at"`
		}
		if len(raw) > 8192 || json.Unmarshal(raw, &h) != nil {
			r.Reason = "pkiturn health 格式無效"
		} else if !h.LastSucceeded || h.Started.IsZero() || h.Finished.IsZero() || healthNow.Before(h.Started) || healthNow.Before(h.Finished) || healthNow.Sub(h.Finished) > 30*time.Second || (h.Running && healthNow.Sub(h.Started) > 30*time.Second) {
			r.Status = Fail
			r.Reason = "pkiturn sweep 未成功或超過 30 秒"
		} else {
			r.Status = Pass
			r.Reason = "最近 30 秒內已有成功 sweep"
		}
	}
	out.Results = append(out.Results, r)
	r = result(t.Service, "pki/root-crl-installed", "credential", true, now)
	raw, code, err = run("exec", "deployment/pkiturn", "-c", "pkiturn", "--", "sh", "-c", `test -n "$PKI_TURN_APP_CRL_MANIFEST" && cat "$PKI_TURN_APP_CRL_MANIFEST"`)
	if err != nil || code != 0 {
		r.Reason = "無法讀取 installed App CRL manifest"
		out.Results = append(out.Results, r)
		return out
	}
	var members []struct {
		Issuer struct {
			ID     string `json:"issuer_id"`
			Kind   string `json:"kind"`
			Domain string `json:"trust_domain"`
		} `json:"issuer"`
		Path string `json:"state_path"`
	}
	if len(raw) > 1<<20 || json.Unmarshal(raw, &members) != nil || len(members) == 0 || len(members) > 128 {
		r.Reason = "App CRL manifest 格式無效"
		out.Results = append(out.Results, r)
		return out
	}
	roots := 0
	r.Status = Pass
	r.Reason = "installed Root CRL 剩餘超過 24 小時"
	for _, m := range members {
		if m.Issuer.Kind != "root" {
			continue
		}
		roots++
		if m.Issuer.ID == "" || m.Issuer.Domain != "app" || !filepath.IsAbs(m.Path) || filepath.Clean(m.Path) != m.Path || !strings.HasPrefix(m.Path, "/run/pki-state/") || !strings.HasSuffix(m.Path, ".json") {
			r.Status = Unknown
			r.Reason = "Root CRL state 引用無效"
			break
		}
		raw, code, err = run("exec", "deployment/pkiturn", "-c", "pkiturn", "--", "cat", "--", m.Path)
		var state struct {
			CRL struct {
				ID   string    `json:"issuer_id"`
				Next time.Time `json:"next_update"`
			} `json:"crl"`
		}
		if err != nil || code != 0 || len(raw) > 1<<20 || json.Unmarshal(raw, &state) != nil || state.CRL.ID != m.Issuer.ID || state.CRL.Next.IsZero() {
			r.Status = Unknown
			r.Reason = "無法驗證 installed Root CRL 狀態"
			break
		}
		if !state.CRL.Next.After(now.Add(24 * time.Hour)) {
			r.Status = Fail
			r.Reason = "App Root CRL 已過期或剩餘不足 24 小時"
			break
		}
		if r.ExpiresAt == nil || state.CRL.Next.Before(*r.ExpiresAt) {
			next := state.CRL.Next
			r.ExpiresAt = &next
		}
	}
	if roots == 0 {
		r.Status = Unknown
		r.Reason = "manifest 缺少 reviewed offline Root"
	}
	out.Results = append(out.Results, r)
	return out
}

func sourceGap(service, id, reason string, required bool, now time.Time) Collection {
	return Collection{Results: []Result{func() Result { r := result(service, id, "coverage", required, now); r.Reason = reason; return r }()}}
}
func countReason(n int) string { return fmt.Sprintf("採集 %d 個有效樣本", n) }
