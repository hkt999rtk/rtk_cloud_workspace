package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/recovery"
)

func runBaseBackup(args []string) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		fmt.Println("base-backup create|restore --config FILE --confirm-environment ENV --confirm-stack STACK --id ID [--destination NEW_DIRECTORY --identity FILE [--pitr-plan FILE]]")
		return nil
	}
	action := args[0]
	if action != "create" && action != "restore" {
		return errors.New("base-backup requires create or restore")
	}
	fs := flag.NewFlagSet("base-backup", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	config := fs.String("config", "", "reviewed physical backup configuration")
	environment := fs.String("confirm-environment", "", "confirmed environment")
	stack := fs.String("confirm-stack", "", "confirmed stack")
	id := fs.String("id", "", "immutable backup id")
	var destination, identity, pitrPlan string
	if action == "restore" {
		fs.StringVar(&pitrPlan, "pitr-plan", "", "reviewed explicit WAL recovery target")
		fs.StringVar(&destination, "destination", "", "new private recovery directory")
		fs.StringVar(&identity, "identity", "", "private age identity file")
	}
	if fs.Parse(args[1:]) != nil || fs.NArg() != 0 || *config == "" || *id == "" || (action == "restore" && (destination == "" || identity == "")) {
		return errors.New("invalid base-backup arguments; use --help")
	}
	f, err := os.Open(*config)
	if err != nil {
		return err
	}
	defer f.Close()
	var c recovery.BaseBackupConfig
	if err = recovery.Decode(io.LimitReader(f, 1<<20), &c); err != nil {
		return err
	}
	if err = c.Validate(); err != nil {
		return err
	}
	if *environment != c.WAL.Environment || *stack != c.WAL.Stack {
		return errors.New("explicit physical backup environment/stack confirmation required")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	e := recovery.BaseBackupEngine{Config: c}
	if action == "create" {
		err = e.Create(ctx, *id)
	} else {
		if pitrPlan == "" {
			err = e.Restore(ctx, *id, destination, identity)
		} else {
			pf, openErr := os.Open(pitrPlan)
			if openErr != nil {
				return openErr
			}
			defer pf.Close()
			var plan recovery.PITRPlan
			if err = recovery.Decode(io.LimitReader(pf, 1<<20), &plan); err != nil {
				return err
			}
			err = e.RestorePITR(ctx, *id, destination, identity, plan)
		}
	}
	if err != nil {
		return err
	}
	status := action + "-complete"
	if pitrPlan != "" {
		status = "pitr-prepared-not-replayed"
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]string{"status": status, "backup_id": *id})
}
