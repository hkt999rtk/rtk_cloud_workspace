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

func runWALArchive(args []string) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		fmt.Println("wal-archive --config FILE --confirm-environment ENV --confirm-stack STACK --name WAL_NAME --source WAL_PATH")
		return nil
	}
	fs := flag.NewFlagSet("wal-archive", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	path := fs.String("config", "", "reviewed WAL configuration")
	environment := fs.String("confirm-environment", "", "confirmed target")
	stack := fs.String("confirm-stack", "", "confirmed target")
	name := fs.String("name", "", "PostgreSQL archive %f")
	source := fs.String("source", "", "PostgreSQL archive %p")
	if fs.Parse(args) != nil || fs.NArg() != 0 || *path == "" || *source == "" || *name == "" {
		return errors.New("invalid wal-archive arguments; use --help")
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
	if err = recovery.ArchiveWAL(ctx, cfg, *name, *source); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]string{"status": "archived", "wal_name": *name})
}
