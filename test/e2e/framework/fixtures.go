package framework

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

const (
	DriverNamespace = "dra-driver-sriov"
	DriverLabel     = "app.kubernetes.io/name=dra-driver-sriov-chart"
)

// RepoRoot returns the repository root (directory containing go.mod).
func RepoRoot() (string, error) {
	if root := os.Getenv("E2E_REPO_ROOT"); root != "" {
		return root, nil
	}
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("failed to resolve caller path")
	}
	dir := filepath.Dir(filename)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found from %s", filename)
		}
		dir = parent
	}
}

// DemoPath returns the absolute path to a file under demo/.
func DemoPath(parts ...string) (string, error) {
	root, err := RepoRoot()
	if err != nil {
		return "", err
	}
	elems := append([]string{root, "demo"}, parts...)
	path := filepath.Join(elems...)
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("demo fixture %s: %w", path, err)
	}
	return path, nil
}
