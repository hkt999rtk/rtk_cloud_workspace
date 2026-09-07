package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/recovery"
)

func runWALArchiveConfig(args []string) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		fmt.Println("wal-archive-config --config FILE --executable PATH --output NEW_FILE --confirm-environment ENV --confirm-stack STACK [--archive-timeout-seconds 60]")
		return nil
	}
	fs := flag.NewFlagSet("wal-archive-config", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	config := fs.String("config", "", "reviewed WAL config")
	executable := fs.String("executable", "", "absolute recovery CLI path on PostgreSQL host")
	output := fs.String("output", "", "new PostgreSQL fragment")
	environment := fs.String("confirm-environment", "", "confirmed scope")
	stack := fs.String("confirm-stack", "", "confirmed scope")
	timeout := fs.Int("archive-timeout-seconds", 60, "native PostgreSQL segment switch interval")
	if fs.Parse(args) != nil || fs.NArg() != 0 || *config == "" || *executable == "" || *output == "" {
		return errors.New("invalid wal-archive-config arguments; use --help")
	}
	f, err := os.Open(*config)
	if err != nil {
		return err
	}
	defer f.Close()
	var c recovery.WALConfig
	if err = recovery.Decode(io.LimitReader(f, 1<<20), &c); err != nil {
		return err
	}
	if *environment != c.Environment || *stack != c.Stack {
		return errors.New("explicit WAL environment/stack confirmation required")
	}
	fragment, err := recovery.RenderWALArchiveConfig(c, *config, *executable, *timeout)
	if err != nil {
		return err
	}
	out, err := os.OpenFile(*output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			os.Remove(*output)
		}
	}()
	if _, err = out.WriteString(fragment); err == nil {
		err = out.Sync()
	}
	closeErr := out.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	directory, err := os.Open(filepath.Dir(*output))
	if err != nil {
		return err
	}
	defer directory.Close()
	if err = directory.Sync(); err != nil {
		return err
	}
	ok = true
	return nil
}
