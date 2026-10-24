package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type environmentProgressLogKey struct{}

// Only the sanitized progress renderer and report write here, never command
// arguments, subprocess output, resource bodies or credentials.
type environmentProgressLog struct {
	mu   sync.Mutex
	file *os.File
	err  error
}

func openEnvironmentProgressLog(environment, action string) (*environmentProgressLog, error) {
	switch action {
	case "preflight", "plan", "create", "upgrade", "metrics":
	default:
		return nil, errors.New("unsupported deployment log operation")
	}
	store, err := newSecretStore("", environment)
	if err != nil {
		return nil, err
	}
	directory := filepath.Join(store.Root, "deployment", "logs")
	for _, path := range []string{store.ConfigRoot, store.Root, filepath.Join(store.Root, "deployment"), directory} {
		info, err := os.Lstat(path)
		if err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700) {
			return nil, errors.New("deployment log directories must be real private directories")
		}
		if err := ensurePrivateDirectory(path); err != nil {
			return nil, err
		}
	}
	file, err := os.CreateTemp(directory, action+"-"+time.Now().UTC().Format("20060102T150405Z")+"-*.log")
	if err != nil {
		return nil, err
	}
	return &environmentProgressLog{file: file}, nil
}

func (l *environmentProgressLog) Write(data []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	n, err := l.file.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if l.err == nil {
		l.err = err
	}
	return n, err
}

func (l *environmentProgressLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return errors.Join(l.err, l.file.Sync(), l.file.Close())
}
