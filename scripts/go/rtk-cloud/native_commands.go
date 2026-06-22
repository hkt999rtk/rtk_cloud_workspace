package main

import (
	"errors"
	"flag"
	"os"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/envroot"
)

func runDeploy(args []string) error {
	fs := flag.NewFlagSet("deploy", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	workspaceFlag := fs.String("workspace", "", "workspace")
	envRootFlag := fs.String("env-root", "", "environment root")
	fs.StringVar(envRootFlag, "secrets-root", "", "deprecated environment root")
	sshKey := fs.String("ssh-key", "", "retired VM SSH key")
	dnsRoot := fs.String("dns-root-domain", "", "DNS root domain override")
	fs.String("video-release", "", "retired VM release")
	fs.String("account-release", "", "retired VM release")
	fs.String("account-release-bundle", "", "retired VM release bundle")
	fs.String("admin-release", "", "retired VM release")
	fs.String("admin-release-bundle", "", "retired VM release bundle")
	loggerOnly := fs.Bool("logger-only", false, "install and verify only logger backend and log forwarders")
	videoOnly := fs.Bool("video-only", false, "deploy only Video Cloud")
	binaryOnly := fs.Bool("binary-only", false, "fast path: update only Video Cloud API binaries")
	localBuild := fs.Bool("local-build", false, "build a local Linux x86_64 Video Cloud bundle before deploy")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *envRootFlag == "" {
		return errors.New("--env-root is required")
	}
	if *binaryOnly && !*videoOnly {
		*videoOnly = true
	}
	if *binaryOnly && *loggerOnly {
		return errors.New("--binary-only cannot be combined with --logger-only")
	}
	if *localBuild && *loggerOnly {
		return errors.New("--local-build cannot be combined with --logger-only")
	}
	workspace := *workspaceFlag
	var err error
	if workspace == "" {
		workspace, err = workspaceRoot()
		if err != nil {
			return err
		}
	}
	envRoot, err := resolveEnvRoot(workspace, *envRootFlag)
	if err != nil {
		return err
	}
	paths := newProvisionPaths(workspace, envRoot, provisionOptions{})
	env, err := envroot.Load(envRoot, *dnsRoot)
	if err != nil {
		return err
	}
	provider, err := newCloudProvider(env.Values["CLOUD_PROVIDER"])
	if err != nil {
		return err
	}
	return provider.RunProvision(provisionContext{
		Paths: paths,
		Env:   env.Values,
		Opts: provisionOptions{
			mode:       provisionMode{preflight: true, apply: true, deploy: true},
			localBuild: *localBuild,
			loggerOnly: *loggerOnly,
			videoOnly:  *videoOnly,
			binaryOnly: *binaryOnly,
			sshKey:     *sshKey,
		},
	})
}
