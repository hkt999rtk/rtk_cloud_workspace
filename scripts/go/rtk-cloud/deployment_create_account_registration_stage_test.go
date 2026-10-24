package main

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func accountRegistrationStageFixture(t *testing.T) (environmentCreateBootstrapPlan, []deploymentplan.Definition, []deploymentplan.Object, deploymentplan.Inputs) {
	t.Helper()
	cfg, p, s, certificate, key, crl, original := accountRegistrationFixture(t)
	definitions, err := renderEnvironmentCreateAccountRegistration(cfg, p, s, certificate, key, crl, original)
	if err != nil {
		t.Fatal(err)
	}
	live := make([]deploymentplan.Object, 0, len(original))
	for i, definition := range original {
		object := deploymentplan.Clone(definition.Object)
		meta := deploymentplan.Map(object["metadata"])
		meta["uid"] = fmt.Sprintf("%08x-1111-4111-8111-%012x", i+1, i+1)
		labels := deploymentplan.Map(meta["labels"])
		if labels == nil {
			labels = deploymentplan.Object{}
			meta["labels"] = labels
		}
		labels["rtk.realtek.com/stack"] = p.Stack
		live = append(live, object)
	}
	return p, definitions, live, deploymentplan.Inputs{Environment: p.Environment, Stack: p.Stack, Operation: "scoped", ConfigurationSHA256: deploymentplan.Digest(cfg), ReleaseSHA256: deploymentplan.Digest(p), BindingsSHA256: deploymentplan.Digest(definitions)}
}

func TestCreateAccountRegistrationStageCompilesWithExactRetainedCoreDependencies(t *testing.T) {
	p, definitions, live, inputs := accountRegistrationStageFixture(t)
	before := deploymentplan.Digest(definitions)
	stage, err := environmentCreateAccountRegistrationStage(p, definitions)
	if err != nil {
		t.Fatal(err)
	}
	original := map[string]deploymentplan.Definition{}
	for _, d := range definitions {
		original[deploymentplan.ID(d.Object)] = d
	}
	retained := map[string]bool{}
	for _, d := range stage {
		id := deploymentplan.ID(d.Object)
		if d.Retain {
			retained[id] = true
			if deploymentplan.Digest(d.Object) != deploymentplan.Digest(original[id].Object) || deploymentplan.Digest(d.Dependencies) != deploymentplan.Digest(original[id].Dependencies) || d.Phase != original[id].Phase {
				t.Fatal("retained dependency was rewritten", id)
			}
		}
	}
	for _, name := range []string{"pki-controller", "certissuer"} {
		if !retained["Deployment/"+p.Stack+"-video-cloud/"+name] {
			t.Fatal("missing authenticated cross-namespace prerequisite", name)
		}
	}
	if len(retained) != 2 {
		t.Fatal("registration adopted unrelated outside resources", len(retained))
	}
	plan, err := deploymentplan.Compile(inputs, stage, live)
	if err != nil {
		t.Fatal("real registration fixture still has absent/cyclic/later-stage dependencies", err)
	}
	secret, account := -1, -1
	secretID := "Secret/" + p.Stack + "-account-manager/account-manager-service-registration-tls"
	for i, r := range plan.Resources {
		if r.ID == secretID {
			secret = i
			if r.Action != "create" {
				t.Fatal("registration identity secret was not fresh")
			}
		}
		if r.ID == "Deployment/"+p.Stack+"-account-manager/account-manager" {
			account = i
			if !slices.Contains(r.Dependencies, secretID) {
				t.Fatal("new Secret not a declared prerequisite")
			}
		}
		if retained[r.ID] && r.Action != "retain" {
			t.Fatal("cross-namespace prerequisite became writable", r.ID)
		}
	}
	if secret < 0 || account <= secret {
		t.Fatal("Account Manager can start before registration TLS Secret")
	}
	if before != deploymentplan.Digest(definitions) {
		t.Fatal("stage selection or compiler modified input recipes")
	}
}

func TestCreateAccountRegistrationStageRejectsUnqualifiedDependencies(t *testing.T) {
	for _, mode := range []string{"missing-dependency", "duplicate-dependency", "retired-dependency", "cyclic-dependency", "later-phase", "changed-retained-body", "missing-retained-owner"} {
		t.Run(mode, func(t *testing.T) {
			p, definitions, live, inputs := accountRegistrationStageFixture(t)
			controller := "Deployment/" + p.Stack + "-video-cloud/pki-controller"
			account := "Deployment/" + p.Stack + "-account-manager/account-manager"
			for i := range definitions {
				if deploymentplan.ID(definitions[i].Object) != controller {
					continue
				}
				switch mode {
				case "missing-dependency":
					definitions = append(definitions[:i], definitions[i+1:]...)
				case "duplicate-dependency":
					definitions = append(definitions, definitions[i])
				case "retired-dependency":
					definitions[i].Retire = true
				case "cyclic-dependency":
					definitions[i].Dependencies = append(slices.Clone(definitions[i].Dependencies), account)
				case "later-phase":
					definitions[i].Phase = "routing"
				}
				break
			}
			for i := range live {
				if deploymentplan.ID(live[i]) != controller {
					continue
				}
				if mode == "changed-retained-body" {
					deploymentplan.Map(certIssuerObjectList(environmentPKIPod(live[i])["containers"])[0])["image"] = "registry.example/foreign@sha256:" + strings.Repeat("a", 64)
				}
				if mode == "missing-retained-owner" {
					live = append(live[:i], live[i+1:]...)
				}
				break
			}
			stage, err := environmentCreateAccountRegistrationStage(p, definitions)
			if err == nil {
				_, err = deploymentplan.Compile(inputs, stage, live)
			}
			if err == nil {
				t.Fatal("unqualified dependency closure compiled", mode)
			}
		})
	}
}
