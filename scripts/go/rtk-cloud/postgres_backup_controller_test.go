package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/postgresbackup"
)

// Exercise the real cluster-lock subprocess boundary while all API responses
// remain local fixtures. The wrapper invokes only this test binary.
func postgresControllerLockFixture(t *testing.T) string {
	t.Helper()
	dir:=t.TempDir();binary,err:=os.Executable();if err!=nil{t.Fatal(err)}
	wrapper:=filepath.Join(dir,"kubectl")
	script:="#!/bin/sh\nexec '"+strings.ReplaceAll(binary,"'","'\\''")+"' -test.run=^TestPostgresBackupControllerLockProcess$ -- \"$@\"\n"
	if err=os.WriteFile(wrapper,[]byte(script),0700);err!=nil{t.Fatal(err)}
	t.Setenv("RTK_CLOUD_KUBECTL",wrapper)
	t.Setenv("RTK_POSTGRES_CONTROLLER_PROCESS","1")
	t.Setenv("RTK_POSTGRES_CONTROLLER_LOCK_DIR",dir)
	return dir
}

func TestPostgresBackupControllerLockProcess(t *testing.T) {
	if os.Getenv("RTK_POSTGRES_CONTROLLER_PROCESS")!="1" {return}
	args:=strings.Join(os.Args," ");path:=filepath.Join(os.Getenv("RTK_POSTGRES_CONTROLLER_LOCK_DIR"),"lock.json")
	fail:=func(){os.Exit(8)}
	switch {
	case strings.Contains(args," create -f - "):
		var object map[string]any;if json.NewDecoder(os.Stdin).Decode(&object)!=nil{fail()}
		object["metadata"].(map[string]any)["uid"]="controller-fixture-uid"
		f,err:=os.OpenFile(path,os.O_CREATE|os.O_EXCL|os.O_WRONLY,0600);if err!=nil{fail()}
		if json.NewEncoder(f).Encode(object)!=nil{fail()};f.Close();json.NewEncoder(os.Stdout).Encode(object)
	case strings.Contains(args," get configmap rtk-core-recovery "):
		if os.Getenv("RTK_POSTGRES_CONTROLLER_MAINTENANCE")=="1" {io.WriteString(os.Stdout,`{"metadata":{"name":"rtk-core-recovery"}}`)}
	case strings.Contains(args," delete --raw "):
		var options struct{Preconditions struct{UID string}};if json.NewDecoder(os.Stdin).Decode(&options)!=nil||options.Preconditions.UID!="controller-fixture-uid"{fail()};if os.Remove(path)!=nil{fail()}
	default:fail()
	}
	os.Exit(0)
}

type postgresControllerFake struct {
	t *testing.T
	c postgresBackupDeployment
	calls []string
	applied []map[string]any
	sourceResponse string
	hbaPath string
	reload string
	status string
	job string
	logs string
	fail string
	roleChanged bool
	enabled bool
	secretRemoved bool
}

func (f *postgresControllerFake) run(ctx context.Context,args []string,in io.Reader,out io.Writer)error {
	command:=" "+strings.Join(args," ")+" ";f.calls=append(f.calls,command)
	var b []byte;if in!=nil{b,_=io.ReadAll(in)}
	if f.fail!=""&&strings.Contains(command,f.fail){return errors.New("fixture-only-secret error")}
	write:=func(v any)error{return json.NewEncoder(out).Encode(v)}
	switch {
	case strings.Contains(command," get pod "):
		return write(map[string]any{"spec":map[string]any{"volumes":[]any{map[string]any{"persistentVolumeClaim":map[string]string{"claimName":f.c.SourcePVC}}}},"status":map[string]any{"containerStatuses":[]any{map[string]any{"name":f.c.SourceContainer,"ready":true,"imageID":"docker-pullable://"+f.c.Worker.Source.PostgresImage}}}})
	case strings.Contains(command," get pvc "):
		return write(map[string]any{"status":map[string]any{"capacity":map[string]string{"storage":"20Gi"}}})
	case strings.Contains(command," get networkpolicy "):
		var from []any;for _,suffix:=range []string{"video-cloud","account-manager","billing"}{from=append(from,map[string]any{"namespaceSelector":map[string]any{"matchLabels":map[string]string{"kubernetes.io/metadata.name":f.c.Stack+"-"+suffix}}})}
		return write(map[string]any{"metadata":map[string]any{"labels":map[string]string{"rtk.realtek.com/stack":f.c.Stack}},"spec":map[string]any{"podSelector":map[string]any{"matchLabels":map[string]string{"app.kubernetes.io/name":"postgresql"}},"ingress":[]any{map[string]any{"from":from,"ports":[]any{map[string]any{"port":5432,"protocol":"TCP"}}}}}})
	case strings.Contains(command," -- psql "):
		sql:=string(b)
		switch {
		case strings.Contains(sql,"pg_control_system"):
			response:=f.sourceResponse;if response==""{response="16|"+f.c.Worker.Source.SystemIdentifier+"|replica|10|10"};_,err:=io.WriteString(out,response);return err
		case strings.Contains(sql,"ALTER ROLE"):
			f.roleChanged=true;if strings.Contains(command,"PASSWORD"){f.t.Fatal("credential-bearing SQL placed in argv")};return nil
		case strings.Contains(sql,"pg_read_file"):
			_,err:=io.WriteString(out,"local all all trust\nhost all all all scram-sha-256\n");return err
		case strings.Contains(sql,"SHOW hba_file"):
			path:=f.hbaPath;if path==""{path="/var/lib/postgresql/data/pgdata/pg_hba.conf"};_,err:=io.WriteString(out,path);return err
		case strings.Contains(sql,"pg_reload_conf"):
			response:=f.reload;if response==""{response="0\nt\n"};_,err:=io.WriteString(out,response);return err
		default:return errors.New("unhandled SQL fixture")
		}
	case strings.Contains(command," -- sh "):
		if !strings.Contains(string(b),"host replication rtk_postgres_backup all scram-sha-256"){f.t.Fatal("replication HBA rule missing")};return nil
	case strings.Contains(command," get configmap "):
		_,err:=io.WriteString(out,f.status);return err
	case strings.Contains(command," get secret "):
		return write(map[string]any{"type":"kubernetes.io/dockerconfigjson","data":map[string]string{".dockerconfigjson":"e30="}})
	case strings.Contains(command," get job "):
		response:=f.job;if response==""{response=`{"status":{"conditions":[{"type":"Complete","status":"True"}]}}`};_,err:=io.WriteString(out,response);return err
	case strings.Contains(command," logs "):
		_,err:=io.WriteString(out,f.logs);return err
	case strings.Contains(command," delete secret "):
		f.secretRemoved=true;return nil
	case strings.Contains(command," patch cronjob "):
		var patch struct{Spec struct{Suspend bool}};if json.Unmarshal(b,&patch)!=nil{return errors.New("bad patch")};f.enabled=!patch.Spec.Suspend;return nil
	case strings.Contains(command," apply -f - ")||strings.Contains(command," create -f - "):
		var object map[string]any;if json.Unmarshal(b,&object)!=nil{return errors.New("invalid applied object")};f.applied=append(f.applied,object)
		if object["kind"]=="ConfigMap"&&object["metadata"].(map[string]any)["name"]==postgresBackupStatusName{blob,_:=json.Marshal(object);f.status=string(blob)}
		return nil
	default:return errors.New("unhandled kubectl fixture")
	}
}

func postgresControllerFixture(t *testing.T)(*postgresBackupManager,*postgresControllerFake,string){
	t.Helper();c:=postgresBackupTestConfig(t);lockdir:=postgresControllerLockFixture(t)
	store,err:=newSecretStore(t.TempDir(),c.Environment);if err!=nil{t.Fatal(err)}
	for _,prefix:=range []string{"RTK_POSTGRES_BACKUP_","RTK_POSTGRES_RESTORE_"}{for _,name:=range []string{"ACCESS_KEY_ID","SECRET_ACCESS_KEY"}{if err=store.write("operator/env/"+prefix+name,[]byte("fixture-value"),false);err!=nil{t.Fatal(err)}}}
	f:=&postgresControllerFake{t:t,c:c};m:=&postgresBackupManager{Config:c,Store:store,Kubeconfig:"/fixture/kubeconfig",Exec:f.run};return m,f,lockdir
}

func controllerStatusObject(t *testing.T,s postgresbackup.Status)string{t.Helper();b,err:=json.Marshal(s);if err!=nil{t.Fatal(err)};v,err:=json.Marshal(map[string]any{"data":map[string]string{"status.json":string(b)}});if err!=nil{t.Fatal(err)};return string(v)}

func TestPostgresBackupControllerConfigureSuspendsAndReleasesLock(t *testing.T){
	m,f,lockdir:=postgresControllerFixture(t)
	if err:=m.configure(context.Background(),false,"");err!=nil{t.Fatal(err)}
	if !f.roleChanged||f.enabled{t.Fatal("configure did not prepare role or enabled schedule early")}
	cronFound:=false;for _,object:=range f.applied{if object["kind"]=="CronJob"{cronFound=true;if object["spec"].(map[string]any)["suspend"]!=true{t.Fatal("initial CronJob not suspended")}}}
	if !cronFound{t.Fatal("schedule missing")};if _,err:=os.Stat(filepath.Join(lockdir,"lock.json"));!os.IsNotExist(err){t.Fatal("configure leaked command lock")}
	password,err:=m.Store.readRuntime("postgres-backup");if err!=nil||len(password)!=64{t.Fatal("backup-specific password not provisioned")}
	if err=m.configure(context.Background(),false,"");err!=nil{t.Fatal(err)};again,_:=m.Store.readRuntime("postgres-backup");if again!=password{t.Fatal("configure rotated existing source credential")}
	status,err:=postgresBackupReadStatus(context.Background(),m);if err!=nil||status.Environment!=m.Config.Environment||status.Latest!=nil{t.Fatal("initial status incorrectly claims backup success")}
}

func TestPostgresBackupControllerConfigureFailureKeepsScheduleClosed(t *testing.T){
	for _,mode:=range []string{"maintenance","source-identity","networkpolicy","hba-location","reload","apply"}{t.Run(mode,func(t *testing.T){
		m,f,lockdir:=postgresControllerFixture(t)
		switch mode{case "maintenance":t.Setenv("RTK_POSTGRES_CONTROLLER_MAINTENANCE","1");case "source-identity":f.sourceResponse="16|wrong|replica|10|10";case "networkpolicy":f.fail=" get networkpolicy ";case "hba-location":f.hbaPath="/different/pg_hba.conf";case "reload":f.reload="1\nf";case "apply":f.fail=" apply -f - "}
		if err:=m.configure(context.Background(),true,"");err==nil||strings.Contains(err.Error(),"fixture-only-secret"){t.Fatal("failure accepted or sensitive subprocess error leaked")};if f.enabled{t.Fatal("failure enabled schedule")}
		if _,err:=os.Stat(filepath.Join(lockdir,"lock.json"));!os.IsNotExist(err){t.Fatal("failed configure retained owned command lock")}
		if (mode=="maintenance"||mode=="source-identity"||mode=="networkpolicy")&&f.roleChanged{t.Fatal("source was mutated before required preflight")}
	})}
}

func TestPostgresBackupControllerReadStatusBoundaries(t *testing.T){
	m,f,_:=postgresControllerFixture(t)
	if s,err:=postgresBackupReadStatus(context.Background(),m);err!=nil||s.Version!=0{t.Fatal("absent status mishandled")}
	valid:=postgresbackup.Status{Version:1,Environment:m.Config.Environment,Stack:m.Config.Stack,ClusterID:m.Config.Worker.ClusterID}
	f.status=controllerStatusObject(t,valid);if _,err:=postgresBackupReadStatus(context.Background(),m);err!=nil{t.Fatal(err)}
	valid.Environment="prod";for _,raw:=range []string{"not JSON",`{"data":{"status.json":"oops"}}`,controllerStatusObject(t,valid)}{f.status=raw;if _,err:=postgresBackupReadStatus(context.Background(),m);err==nil{t.Fatal("invalid or foreign status accepted")}}
	f.fail=" get configmap ";if _,err:=postgresBackupReadStatus(context.Background(),m);err==nil{t.Fatal("unavailable status accepted")}
}

func TestPostgresBackupControllerJobAndWaitFailures(t *testing.T){
	for _,action:=range []string{"run","retry-upload","prune"}{t.Run(action,func(t *testing.T){m,f,_:=postgresControllerFixture(t);if err:=m.createJob(context.Background(),action,"backup-1");err!=nil{t.Fatal(err)};if len(f.applied)!=1||f.applied[0]["kind"]!="Job"{t.Fatal("expected exactly one job")};encoded,_:=json.Marshal(f.applied[0]);if !bytes.Contains(encoded,[]byte(action)){t.Fatal("job action missing")}})}
	m,f,_:=postgresControllerFixture(t);f.sourceResponse="16|wrong|replica|10|10";if err:=m.createJob(context.Background(),"run","");err==nil||len(f.applied)!=0{t.Fatal("wrong source created a capture Job")}
	for _,tc:=range []struct{name,response string;success bool}{{"complete",`{"status":{"conditions":[{"type":"Complete","status":"True"}]}}`,true},{"failed",`{"status":{"conditions":[{"type":"Failed","status":"True"}]}}`,false},{"malformed","bad JSON",false},{"cancelled",`{"status":{}}`,false}}{t.Run(tc.name,func(t *testing.T){m,f,_:=postgresControllerFixture(t);f.job=tc.response;ctx,cancel:=context.WithCancel(context.Background());defer cancel();if tc.name=="cancelled"{cancel()};err:=m.waitDrill(ctx,"isolated");if (err==nil)!=tc.success{t.Fatalf("unexpected wait result: %v",err)}})}
}

func TestPostgresBackupControllerEnableRequiresMatchingQualification(t *testing.T){
	m,f,_:=postgresControllerFixture(t);c:=m.Config
	s:=postgresbackup.Status{Version:1,Environment:c.Environment,Stack:c.Stack,ClusterID:c.Worker.ClusterID,Latest:&postgresbackup.Completion{Manifest:postgresbackup.Manifest{PostgresImage:c.Worker.Source.PostgresImage}},LatestDrill:&postgresbackup.Drill{Version:1,Environment:c.Environment,Stack:c.Stack,ClusterID:c.Worker.ClusterID,BackupID:"backup-1",SystemIdentifier:c.Worker.Source.SystemIdentifier,PostgresImage:c.Worker.Source.PostgresImage,Success:true}}
	f.status=controllerStatusObject(t,s)
	q:=postgresBackupQualification{Version:1,Environment:c.Environment,Stack:c.Stack,ClusterID:c.Worker.ClusterID,SourcePostgresImage:c.Worker.Source.PostgresImage,RunnerImage:c.RunnerImage,SystemIdentifier:c.Worker.Source.SystemIdentifier,MaxRate:"10M",BackupID:"backup-1",WorkloadID:"fixture-load",ReviewedBy:"fixture-reviewer",MeasuredAt:time.Now().Add(-time.Hour),Baseline:postgresBackupLoadMetrics{DurationSeconds:300,Requests:30000,P95MS:100,P99MS:200},DuringBackup:postgresBackupLoadMetrics{DurationSeconds:300,Requests:30000,P95MS:104,P99MS:209}}
	path:=filepath.Join(t.TempDir(),"qualification.json");b,_:=json.Marshal(q);if err:=os.WriteFile(path,b,0600);err!=nil{t.Fatal(err)}
	if err:=m.configure(context.Background(),true,path);err!=nil{t.Fatal(err)};if !f.enabled{t.Fatal("qualified schedule not enabled")}
	f.enabled=false;q.DuringBackup.P99MS=250;b,_=json.Marshal(q);os.WriteFile(path,b,0600)
	if err:=m.configure(context.Background(),true,path);err==nil||f.enabled{t.Fatal("performance regression enabled schedule")}
}
