package main

import (
	"bytes"
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Isolate only native/bootstrap, compilation and later provider I/O. The real
// create orchestration and its workload apply context execute unchanged.
func TestCreateWorkloadCarriesOriginalUnsealRuntime(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(workspace, "scripts/go/rtk-cloud/deployment_create.go")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	set := token.NewFileSet()
	parsed, err := parser.ParseFile(set, path, raw, 0)
	if err != nil {
		t.Fatal(err)
	}
	start, end := 0, 0
	for _, decl := range parsed.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "executeEnvironmentCreateWithRuntime" {
			start, end = set.Position(fn.Body.Pos()).Offset, set.Position(fn.Body.End()).Offset
		}
	}
	if start == 0 || end <= start {
		t.Fatal("real create orchestration unavailable")
	}
	body := string(raw[start:end])
	for original, replacement := range map[string]string{"executeEnvironmentCreateBootstrap(": "testWorkloadBootstrap(", "compileCreatedEnvironmentTarget(": "testWorkloadCompile(", "finishEnvironmentCreateFreshProducts(": "testWorkloadFinish(", "configureEnvironmentCreateExternalDataPlanes(": "testWorkloadExternal("} {
		if strings.Count(body, original) != 1 {
			t.Fatal("unexpected create orchestration dependency shape")
		}
		body = strings.Replace(body, original, replacement, 1)
	}
	current := append(append(append([]byte{}, raw[:start]...), []byte(body)...), raw[end:]...)
	old := bytes.Replace(current, []byte(`apply(bootstrapCtx, "workload"`), []byte(`apply(ctx, "workload"`), 1)
	if bytes.Equal(current, old) {
		t.Fatal("workload no longer binds its original runtime context")
	}
	dir := t.TempDir()
	virtual := filepath.Join(workspace, "scripts/go/rtk-cloud/zz_workload_context_overlay_test.go")
	if _, err := os.Lstat(virtual); !os.IsNotExist(err) {
		t.Fatal("isolated fixture shadows maintained source")
	}
	physical := filepath.Join(dir, "context_fixture_test.go")
	if err := os.WriteFile(physical, []byte(createWorkloadContextNativeTest), 0600); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"current", "historical"} {
		t.Run(mode, func(t *testing.T) {
			source := current
			if mode == "historical" {
				source = old
			}
			modified := filepath.Join(dir, mode+"_create.go")
			if err := os.WriteFile(modified, source, 0600); err != nil {
				t.Fatal(err)
			}
			overlay := filepath.Join(dir, mode+"_overlay.json")
			data, _ := json.Marshal(map[string]any{"Replace": map[string]string{path: modified, virtual: physical}})
			if err := os.WriteFile(overlay, data, 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			command := exec.CommandContext(ctx, filepath.Join(runtime.GOROOT(), "bin/go"), "test", "-overlay", overlay, "-vet=off", "./rtk-cloud", "-run", "^TestDeploymentWorkload(Context|SkipAccepted)Fixture$", "-count=1")
			command.Dir = filepath.Join(workspace, "scripts/go")
			command.Env = append(os.Environ(), "GOWORK=off", "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local", "VIDEO_CLOUD_TEST_DSN=")
			output, err := command.CombinedOutput()
			if mode == "current" && err != nil {
				t.Fatalf("real workload lost original-share callback context: %v\n%s", err, output)
			}
			if mode == "historical" && (err == nil || !bytes.Contains(output, []byte("original-share callback runtime unavailable"))) {
				t.Fatalf("historical context control did not reproduce missing callback: %v\n%s", err, output)
			}
		})
	}
	if _, err := os.Lstat(virtual); !os.IsNotExist(err) {
		t.Fatal("runtime test modified maintained source")
	}
}

const createWorkloadContextNativeTest = `package main
import (
 "context"
 "errors"
 "io"
 "strings"
 "testing"
 "time"
 "rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)
var testWorkloadReachedExternal=errors.New("isolated later provider boundary")
type testWorkloadOriginalKey struct{}
type testWorkloadOriginal struct{initial,steady *deploymentplan.Plan}
func testWorkloadBootstrap(ctx context.Context,cfg deploymentConfig,p environmentCreateBootstrapPlan,release environmentRelease,store secretStore,operator map[string]string,apply environmentCreateBootstrapApply)(environmentIdentityBindings,error) {return environmentIdentityBindings{},nil}
func testWorkloadCompile(ctx context.Context,cfg deploymentConfig,p *environmentCreatePlan,store secretStore,bindings environmentIdentityBindings,parentSHAs ...string)(*compiledEnvironmentDeployment,error) {if runtime,ok:=ctx.Value(environmentCreateBootstrapRuntimeKey{}).(environmentCreateBootstrapRuntime);!ok||runtime.Execution==nil {return nil,errors.New("target original runtime unavailable")};if original,ok:=ctx.Value(testWorkloadOriginalKey{}).(testWorkloadOriginal);ok{return &compiledEnvironmentDeployment{Plan:original.steady},nil};return &compiledEnvironmentDeployment{Plan:&deploymentplan.Plan{}},nil}
func testWorkloadFinish(ctx context.Context,cfg deploymentConfig,p *environmentCreatePlan,store secretStore,operator map[string]string,e *environmentCreateExecution,c *compiledEnvironmentDeployment,apply environmentCreateBootstrapApply)(*compiledEnvironmentDeployment,error){if original,ok:=ctx.Value(testWorkloadOriginalKey{}).(testWorkloadOriginal);ok{if c.Plan.SHA256()!=original.initial.SHA256(){return nil,errors.New("accepted initial child replaced by steady rendering")};return &compiledEnvironmentDeployment{Plan:original.steady},nil};return c,nil}
func testWorkloadExternal(ctx context.Context,cfg deploymentConfig,p *environmentCreatePlan,receipts map[string]environmentCreateProviderReceipt,store secretStore,operator map[string]string,e *environmentCreateExecution)error {runtime,ok:=ctx.Value(environmentCreateBootstrapRuntimeKey{}).(environmentCreateBootstrapRuntime);if !ok||runtime.Execution!=e||runtime.ExecutionSHA256!=e.Journal.PlanSHA256{return errors.New("original routing runtime unavailable")};return testWorkloadReachedExternal}
func TestDeploymentWorkloadContextFixture(t *testing.T) {
 type parentMarker struct{}
 parent,cancel:=context.WithTimeout(context.WithValue(context.Background(),parentMarker{},"original-parent"),time.Minute);defer cancel()
 deadline,_:=parent.Deadline()
 e:=&environmentCreateExecution{Journal:environmentCreateJournal{PlanSHA256:strings.Repeat("a",64)}}
 runtime:=environmentCreateBootstrapRuntime{ExecutionSHA256:e.Journal.PlanSHA256,Execution:e,OpenBao:environmentCreateOpenBaoBootstrap{RootTokenPath:"original/root-token",CACertPath:"original/ca.crt"}}
 called:=false
 apply:=func(ctx context.Context,stage string,definitions []deploymentplan.Definition)error {
  if stage!="workload" {t.Fatal("unexpected native stage",stage)}
  // This is the production applyEnvironmentCreateStage admission condition
  // that enables the guarded original-share OpenBaoReplacementReady callback.
  got,ok:=ctx.Value(environmentCreateBootstrapRuntimeKey{}).(environmentCreateBootstrapRuntime)
  if !ok || got.ExecutionSHA256!=e.Journal.PlanSHA256 || got.Execution!=e || got.OpenBao.RootTokenPath!=runtime.OpenBao.RootTokenPath || got.OpenBao.CACertPath!=runtime.OpenBao.CACertPath {t.Fatal("original-share callback runtime unavailable")}
  actual,has:=ctx.Deadline()
  if !has || !actual.Equal(deadline) || ctx.Value(parentMarker{})!="original-parent" {t.Fatal("workload changed original parent deadline or authority")}
  called=true;return nil
 }
 err:=executeEnvironmentCreateWithRuntime(parent,deploymentConfig{},&environmentCreatePlan{},nil,secretStore{},nil,e,nil,runtime,apply)
 if !called || !errors.Is(err,testWorkloadReachedExternal) {t.Fatal("real create workflow did not reach isolated post-workload boundary",err)}
}
func TestDeploymentWorkloadSkipAcceptedFixture(t *testing.T){
 store,e,initial,b:=freshStageFixture(t)
 check:=func(ctx context.Context,p *deploymentplan.Plan)error{return e.Check(ctx)}
 if err:=executeEnvironmentCreateStagePlan(t.Context(),store,e,"workload",initial,b,check,check,check,func(context.Context)error{return nil},io.Discard);err!=nil{t.Fatal(err)}
 steady:=*initial;steady.Resources=append([]deploymentplan.Resource(nil),initial.Resources...);steady.Resources[0].Desired=deploymentplan.Clone(initial.Resources[0].Desired);deploymentplan.Map(steady.Resources[0].Desired["data"])["root"]="steady-after-finalization"
 ctx:=context.WithValue(t.Context(),testWorkloadOriginalKey{},testWorkloadOriginal{initial:initial,steady:&steady})
 runtime:=environmentCreateBootstrapRuntime{Execution:e,ExecutionSHA256:e.Journal.PlanSHA256}
 err:=executeEnvironmentCreateWithRuntime(ctx,deploymentConfig{},&environmentCreatePlan{},nil,store,nil,e,io.Discard,runtime,func(context.Context,string,[]deploymentplan.Definition)error{t.Fatal("accepted initial workload was reapplied after finalization");return nil})
 if !errors.Is(err,testWorkloadReachedExternal)||b.writes[initial.Resources[0].ID]!=1{t.Fatal("real fresh retry did not retain initial child and routing context",err)}
}
`
