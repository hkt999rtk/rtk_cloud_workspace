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
		fmt.Println("base-backup create|restore|scheduled|observe --config FILE --confirm-environment ENV --confirm-stack STACK [--id ID | --schedule FILE] [--destination NEW_DIRECTORY --identity FILE [--pitr-plan FILE]]")
		fmt.Println("observe: --id ID --destination PREPARED_DIRECTORY --socket-directory PRIVATE_SOCKET --user USER [--database postgres --port 5432]")
		return nil
	}
	action := args[0]
	if action != "create" && action != "restore" && action != "scheduled" && action != "observe" {
		return errors.New("base-backup requires create, restore, scheduled or observe")
	}
	fs := flag.NewFlagSet("base-backup", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	config := fs.String("config", "", "reviewed physical backup configuration")
	environment := fs.String("confirm-environment", "", "confirmed environment")
	stack := fs.String("confirm-stack", "", "confirmed stack")
	var id, schedule string
	if action == "scheduled" {
		fs.StringVar(&schedule, "schedule", "", "reviewed interval and private scheduler state")
	} else {
		fs.StringVar(&id, "id", "", "immutable backup id")
	}
	var destination, identity, pitrPlan string
	var conn recovery.PITRConnection
	if action == "observe" {
		fs.StringVar(&destination, "destination", "", "prepared recovery directory")
		fs.StringVar(&conn.SocketDirectory, "socket-directory", "", "private local recovery socket")
		fs.IntVar(&conn.Port, "port", 5432, "local PostgreSQL port")
		fs.StringVar(&conn.User, "user", "", "local recovery database user")
		fs.StringVar(&conn.Database, "database", "postgres", "local recovery database")
	}
	if action == "restore" {
		fs.StringVar(&pitrPlan, "pitr-plan", "", "reviewed explicit WAL recovery target")
		fs.StringVar(&destination, "destination", "", "new private recovery directory")
		fs.StringVar(&identity, "identity", "", "private age identity file")
	}
	if fs.Parse(args[1:]) != nil || fs.NArg() != 0 || *config == "" || (action != "scheduled" && id == "") || (action == "scheduled" && schedule == "") || (action == "restore" && (destination == "" || identity == "")) || (action == "observe" && (destination == "" || conn.SocketDirectory == "" || conn.User == "")) {
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
	if action == "observe" {
		observation, err := e.ObservePITR(ctx, id, destination, conn)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(observation)
	}
	if action == "scheduled" {
		sf, openErr := os.Open(schedule)
		if openErr != nil {
			return openErr
		}
		defer sf.Close()
		var policy recovery.BaseBackupSchedule
		if err = recovery.Decode(io.LimitReader(sf, 1<<20), &policy); err != nil {
			return err
		}
		result, runErr := e.Scheduled(ctx, policy)
		if encodeErr := json.NewEncoder(os.Stdout).Encode(result); encodeErr != nil {
			return encodeErr
		}
		return runErr
	}
	if action == "create" {
		err = e.Create(ctx, id)
	} else {
		if pitrPlan == "" {
			err = e.Restore(ctx, id, destination, identity)
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
			err = e.RestorePITR(ctx, id, destination, identity, plan)
		}
	}
	if err != nil {
		return err
	}
	status := action + "-complete"
	if pitrPlan != "" {
		status = "pitr-prepared-not-replayed"
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]string{"status": status, "backup_id": id})
}
