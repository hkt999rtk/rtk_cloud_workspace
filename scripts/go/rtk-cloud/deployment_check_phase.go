package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	deploymentCheckPreDeploy  = "pre-deploy"
	deploymentCheckPostDeploy = "post-deploy"
)

// This scan only selects the opening description. The flag parser below is
// still authoritative and rejects invalid or conflicting phase arguments.
func deploymentCheckBannerPhase(args []string) string {
	phase := deploymentCheckPostDeploy
	selected := ""
	for i := 0; i < len(args); i++ {
		value := ""
		if args[i] == "--phase" || args[i] == "-phase" {
			i++
			if i == len(args) {
				return ""
			}
			value = args[i]
		} else if strings.HasPrefix(args[i], "--phase=") || strings.HasPrefix(args[i], "-phase=") {
			value = strings.SplitN(args[i], "=", 2)[1]
		} else {
			continue
		}
		if value != deploymentCheckPreDeploy && value != deploymentCheckPostDeploy {
			return ""
		}
		if selected != "" && selected != value {
			return ""
		}
		selected, phase = value, value
	}
	return phase
}

func printDeploymentCheckPurpose(out io.Writer, phase string) {
	switch phase {
	case deploymentCheckPreDeploy:
		fmt.Fprintln(out, "部署前可部署性檢查（pre-deploy）：確認完整 create／upgrade 的目標設定、工具與 SecretStore 前置條件；請在整個環境部署前使用。")
		fmt.Fprintln(out, "此檢查唯讀，未驗證寫入權限；舊部署的健康問題不作為新部署阻擋條件，但不可安全遷移的路由會阻擋。不代表目前服務健康；--fast 只減少驗證深度。")
	case deploymentCheckPostDeploy:
		fmt.Fprintln(out, "部署後環境健康檢查（post-deploy）：驗證已部署環境的 Secret 綁定、PKI 與公開入口；請在部署後或排查現況時使用。")
		fmt.Fprintln(out, "不作為部署前可部署性判定，也不取代完整應用驗收。預設 provider 驗證可能使用暫時性 DNS／storage 寫入；唯讀請用 --read-only，--fast 只減少驗證深度。")
	default:
		fmt.Fprintln(out, "部署環境檢查：--phase pre-deploy 用於部署前可部署性；--phase post-deploy 用於部署後環境健康（預設）。")
	}
}

func deploymentCheckScope(phase string) string {
	if phase == deploymentCheckPreDeploy {
		return "read-only full create/upgrade deployability: desired configuration, provision prerequisites, local SecretStore, safe route migration and selected provider/input checks; current runtime health and write permissions unverified; not release approval"
	}
	return "existing deployment health: live SecretStore bindings, PKI/public ingress and selected provider/input checks; not full application acceptance or release approval"
}

func executeDeploymentPreDeployCheck(ctx context.Context, o deploymentCheckOptions, cfg deploymentConfig, deps deploymentCheckDependencies, reporter *deploymentCheckReporter) {
	store, storeErr := deps.store(cfg.Environment)
	store.checkRuntime = newDeploymentCheckRuntime(ctx, cfg.Environment)
	q := o.qualification
	q.readOnly = true
	q.envFile = defaultDeploymentEnvironmentCredentialFile(cfg.Environment)
	tasks := []struct {
		id  string
		run func() error
	}{
		{"secrets.local", func() error {
			if storeErr != nil {
				return errors.New("selected environment SecretStore cannot be resolved")
			}
			return deps.local(store)
		}},
		{"secrets.legacy-paths", func() error { return verifyDeploymentCheckLegacyPaths(store, cfg.Workspace) }},
		{"deployment.preflight", func() error {
			if deps.preflight == nil {
				return errors.New("deployment preflight is unavailable")
			}
			return deps.preflight(ctx, cfg, reporter.out)
		}},
	}
	for _, t := range tasks {
		reporter.emit(deploymentCredentialCheck{ID: t.id, Status: "PENDING", Required: true, Resource: cfg.Environment})
	}
	if deps.plan != nil {
		deps.plan(ctx, cfg, q.envFile, q, reporter.emit)
	}
	for _, t := range tasks {
		if err := ctx.Err(); err != nil {
			reporter.emit(deploymentCheckFailure(t.id, err))
			continue
		}
		reporter.emit(deploymentCredentialCheck{ID: t.id, Status: "RUNNING"})
		if err := t.run(); err != nil {
			reporter.emit(deploymentCheckFailure(t.id, err))
		} else {
			reporter.emit(deploymentCredentialCheck{ID: t.id, Status: "PASS", Passed: true, Required: true, Detail: "verified", Attempts: 1})
		}
	}
	// Local desired-input checks and provider read probes remain independent.
	// Never perform credential repairs, write canaries or write receipts here.
	deps.collect(ctx, cfg, q.envFile, q, false, reporter.emit)
}
