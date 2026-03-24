package taskrunner

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	rt "github.com/docup/agentctl/internal/core/runtime"
)

type runtimeGuardSnapshot struct {
	baseDir string
	roots   []string
	mutable map[string]struct{}
	files   map[string]string
}

func captureRuntimeGuardSnapshot(spec *rt.StageSpec, mutablePaths ...string) (*runtimeGuardSnapshot, error) {
	snapshot := &runtimeGuardSnapshot{
		baseDir: runtimeGuardBaseDir(spec.SessionDir),
		mutable: make(map[string]struct{}, len(mutablePaths)),
		files:   make(map[string]string),
	}

	for _, path := range mutablePaths {
		if path == "" {
			continue
		}
		snapshot.mutable[filepath.Clean(path)] = struct{}{}
	}

	roots := []string{filepath.Clean(spec.SessionDir)}
	if spec.ContextDir != "" && isRuntimeManagedPath(spec.ContextDir) {
		contextDir := filepath.Clean(spec.ContextDir)
		if !strings.HasPrefix(contextDir, roots[0]+string(filepath.Separator)) && contextDir != roots[0] {
			roots = append(roots, contextDir)
		}
	}
	snapshot.roots = uniquePaths(roots)

	for _, root := range snapshot.roots {
		if err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			clean := filepath.Clean(path)
			if _, ok := snapshot.mutable[clean]; ok {
				return nil
			}
			hash, err := hashFile(clean)
			if err != nil {
				return err
			}
			snapshot.files[clean] = hash
			return nil
		}); err != nil {
			return nil, err
		}
	}
	return snapshot, nil
}

func detectRuntimeViolations(snapshot *runtimeGuardSnapshot) ([]string, error) {
	current := make(map[string]string, len(snapshot.files))
	for _, root := range snapshot.roots {
		if err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			clean := filepath.Clean(path)
			if _, ok := snapshot.mutable[clean]; ok {
				return nil
			}
			hash, err := hashFile(clean)
			if err != nil {
				return err
			}
			current[clean] = hash
			return nil
		}); err != nil {
			return nil, err
		}
	}

	violations := make([]string, 0)
	for path, hash := range current {
		original, ok := snapshot.files[path]
		if !ok {
			violations = append(violations, fmt.Sprintf("created %s", snapshot.displayPath(path)))
			continue
		}
		if original != hash {
			violations = append(violations, fmt.Sprintf("modified %s", snapshot.displayPath(path)))
		}
	}
	for path := range snapshot.files {
		if _, ok := current[path]; !ok {
			violations = append(violations, fmt.Sprintf("deleted %s", snapshot.displayPath(path)))
		}
	}

	sort.Strings(violations)
	return violations, nil
}

func (s *runtimeGuardSnapshot) displayPath(path string) string {
	baseParent := filepath.Dir(s.baseDir)
	if rel, err := filepath.Rel(baseParent, path); err == nil {
		return rel
	}
	return path
}

func hashFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func uniquePaths(paths []string) []string {
	seen := make(map[string]struct{}, len(paths))
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		if path == "" {
			continue
		}
		clean := filepath.Clean(path)
		if _, ok := seen[clean]; ok {
			continue
		}
		seen[clean] = struct{}{}
		out = append(out, clean)
	}
	return out
}

func isRuntimeManagedPath(path string) bool {
	clean := filepath.Clean(path)
	for {
		if filepath.Base(clean) == ".agentctl" {
			return true
		}
		parent := filepath.Dir(clean)
		if parent == clean {
			return false
		}
		clean = parent
	}
}

func runtimeGuardBaseDir(sessionDir string) string {
	clean := filepath.Clean(sessionDir)
	for {
		if filepath.Base(clean) == ".agentctl" {
			return clean
		}
		parent := filepath.Dir(clean)
		if parent == clean {
			return filepath.Clean(sessionDir)
		}
		clean = parent
	}
}
