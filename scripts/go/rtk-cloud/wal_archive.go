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
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/recovery"
	"syscall"
)

func runWALArchive(args []string) error { return runWALCommand(args, false) }
func runWALRestore(args []string) error { return runWALCommand(args, true) }

func runWALCommand(args []string, restore bool) error {
	command := "wal-archive"
	if restore {
		command = "wal-restore"
	}
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		if restore {
			fmt.Println("wal-restore --config FILE --confirm-environment ENV --confirm-stack STACK --name WAL_NAME --destination WAL_PATH --identity FILE")
		} else {
			fmt.Println("wal-archive --config FILE --confirm-environment ENV --confirm-stack STACK --name WAL_NAME --source WAL_PATH")
		}
		return nil
	}
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	path := fs.String("config", "", "reviewed WAL configuration")
	environment := fs.String("confirm-environment", "", "confirmed target")
	stack := fs.String("confirm-stack", "", "confirmed target")
	name := fs.String("name", "", "PostgreSQL archive %f")
	var source, destination, identity string
	if restore {
		fs.StringVar(&destination, "destination", "", "PostgreSQL restore %p")
		fs.StringVar(&identity, "identity", "", "private age identity file")
	} else {
		fs.StringVar(&source, "source", "", "PostgreSQL archive %p")
	}
	if fs.Parse(args) != nil || fs.NArg() != 0 || *path == "" || (restore && (destination == "" || identity == "")) || (!restore && source == "") || *name == "" {
		return fmt.Errorf("invalid %s arguments; use --help", command)
	}
	f, err := os.Open(*path)
	if err != nil {
		return err
	}
	defer f.Close()
	var cfg recovery.WALConfig
	if err = recovery.Decode(io.LimitReader(f, 1<<20), &cfg); err != nil {
		return err
	}
	if err = cfg.Validate(); err != nil {
		return err
	}
	if *environment != cfg.Environment || *stack != cfg.Stack {
		return errors.New("explicit WAL environment/stack confirmation required")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	status := "archived"
	if restore {
		status = "restored"
		err = recovery.RestoreWAL(ctx, cfg, *name, destination, identity)
	} else {
		err = recovery.ArchiveWAL(ctx, cfg, *name, source)
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]string{"status": status, "wal_name": *name})
}
