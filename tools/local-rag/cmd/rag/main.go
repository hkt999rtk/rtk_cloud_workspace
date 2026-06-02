package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"local-rag/internal/rag"
)

func main() {
	os.Exit(run())
}

func run() int {
	workspace := flag.String("workspace", envDefault("RTK_RAG_WORKSPACE", mustGetwd()), "workspace path")
	dbPath := flag.String("db", os.Getenv("RTK_RAG_DB"), "SQLite database path")
	flag.Parse()
	if flag.NArg() < 1 {
		usage()
		return 2
	}
	index := rag.NewIndex(*workspace, *dbPath, rag.NewOpenAIClient())
	ctx := context.Background()
	switch flag.Arg(0) {
	case "index":
		full := false
		changed := false
		fs := flag.NewFlagSet("index", flag.ExitOnError)
		fs.BoolVar(&full, "full", false, "full index")
		fs.BoolVar(&changed, "changed", false, "changed index")
		_ = fs.Parse(flag.Args()[1:])
		var result rag.IndexResult
		var err error
		if changed && !full {
			result, err = index.IndexChanged(ctx)
		} else {
			result, err = index.IndexFull(ctx)
		}
		return printJSON(result, err)
	case "status":
		result, err := index.Status()
		return printJSON(result, err)
	case "query":
		if flag.NArg() < 2 {
			fmt.Fprintln(os.Stderr, "query text is required")
			return 2
		}
		result, err := index.Query(ctx, flag.Arg(1), nil, 8)
		return printJSON(result, err)
	default:
		usage()
		return 2
	}
}

func printJSON(value any, err error) int {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println(string(raw))
	return 0
}

func usage() {
	fmt.Fprintf(os.Stderr, "usage: rag [--workspace PATH] [--db PATH] <index|status|query>\n")
}

func envDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func mustGetwd() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	abs, err := filepath.Abs(wd)
	if err != nil {
		return wd
	}
	return abs
}
