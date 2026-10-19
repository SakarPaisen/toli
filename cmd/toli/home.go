package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/arjunvlama/toli/internal/token"
)

// homeDir returns toli's data directory, creating it if needed.
func homeDir() (string, error) {

	toliHome := os.Getenv("TOLI_HOME")
	if toliHome == "" {
		dir, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		toliHome = filepath.Join(dir, ".toli")
	}
	if err := os.MkdirAll(toliHome, 0o700); err != nil {
		return "", fmt.Errorf("create toli home %s: %w", toliHome, err)
	}

	return toliHome, nil
}

// adminTokenFile is where the daemon keeps the admin token for the TUI.
const adminTokenFile = "admin-token"

// loadOrCreateAdminToken is called by the daemon at startup. It reuses the
// token in home/admin-token, or creates one (mode 0600) on first run.
func loadOrCreateAdminToken(home string) (string, error) {

	path := filepath.Join(home, adminTokenFile)

	contents, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("read admin token: %w", err)
	}

	if tok := strings.TrimSpace(string(contents)); tok != "" {
		return tok, nil
	}

	tok, _ := token.New()
	if err := writeFileAtomic(path, []byte(tok+"\n")); err != nil {
		return "", fmt.Errorf("write admin token: %w", err)
	}

	return tok, nil
}

// readAdminToken is called by the TUI to authenticate to the daemon
// ($TOLI_ADMIN_TOKEN, if set, takes precedence; see runTUI).
func readAdminToken(home string) (string, error) {
	path := filepath.Join(home, adminTokenFile)

	contents, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("no admin token at %s; has `toli daemon` been started?", path)
	}
	if err != nil {
		return "", fmt.Errorf("read admin token: %w", err)
	}

	tok := strings.TrimSpace(string(contents))
	if tok == "" {
		return "", fmt.Errorf("admin token file %s is empty; restart `toli daemon` to recreate it", path)
	}
	return tok, nil
}

// writeFileAtomic writes data to path (mode 0600) via a temp file and a
// rename, so a crash mid-write never leaves a partial or empty file behind.
func writeFileAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*") // created 0600
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		os.Remove(f.Name())
	}
	return err
}
