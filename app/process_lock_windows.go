//go:build windows

package app

import (
	"fmt"
	"os"
	"path/filepath"
)

type processLock struct {
	path string
	file *os.File
}

func acquireProcessLock(path string) (*processLock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, fmt.Errorf("create lock directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0644)
	if err != nil {
		if os.IsExist(err) {
			return nil, fmt.Errorf("miya-channels is already running")
		}
		return nil, fmt.Errorf("open channels lock: %w", err)
	}
	_, _ = fmt.Fprintf(file, "%d\n", os.Getpid())
	return &processLock{path: path, file: file}, nil
}

func (l *processLock) Release() {
	if l == nil || l.file == nil {
		return
	}
	_ = l.file.Close()
	_ = os.Remove(l.path)
	l.file = nil
}
