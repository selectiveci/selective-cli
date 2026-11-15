package localrun

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type treeSnapshot struct {
	SHA    string            `json:"sha"`
	Prefix string            `json:"prefix"`
	Files  map[string]string `json:"files"`
	Dirty  []string          `json:"dirty"`
}

func takeSnapshot(dir string) (treeSnapshot, bool) {
	root, ok := gitOutput(dir, "rev-parse", "--show-toplevel")
	if !ok || strings.TrimSpace(root) == "" {
		return treeSnapshot{}, false
	}
	root = strings.TrimSpace(root)
	listing, ok := gitOutput(root, "ls-tree", "-r", "-z", "--full-tree", "HEAD")
	if !ok {
		return treeSnapshot{}, false
	}
	sha, _ := gitOutput(root, "rev-parse", "HEAD")
	snapshot := treeSnapshot{SHA: strings.TrimSpace(sha), Prefix: prefixWithin(root, dir), Files: map[string]string{}}
	for _, entry := range strings.Split(listing, "\x00") {
		meta, path, found := strings.Cut(entry, "\t")
		if fields := strings.Split(meta, " "); found && len(fields) > 2 {
			snapshot.Files[path] = fields[2]
		}
	}
	snapshot.Dirty = foldWorkingTree(root, snapshot.Files)
	return snapshot, true
}

func foldWorkingTree(root string, files map[string]string) []string {
	status, ok := gitOutput(root, "status", "--porcelain=v1", "-z", "--untracked-files=no", "--no-renames")
	dirty := []string{}
	if !ok {
		return dirty
	}
	var changed, deleted, existing []string
	for _, entry := range strings.Split(status, "\x00") {
		if len(entry) < 4 {
			continue
		}
		path := entry[3:]
		info, statErr := os.Stat(filepath.Join(root, path))
		if strings.Contains(entry[:2], "D") && statErr != nil {
			delete(files, path)
			deleted = append(deleted, path)
			continue
		}
		changed = append(changed, path)
		if statErr == nil && info.Mode().IsRegular() {
			existing = append(existing, path)
		}
	}
	for path, blob := range hashObjects(root, existing) {
		files[path] = blob
	}
	return append(changed, deleted...)
}

func hashObjects(root string, paths []string) map[string]string {
	hashed := map[string]string{}
	if len(paths) == 0 {
		return hashed
	}
	cmd := exec.Command("git", "-C", root, "hash-object", "--stdin-paths")
	cmd.Stdin = strings.NewReader(strings.Join(paths, "\n") + "\n")
	out, err := cmd.Output()
	if err != nil {
		return hashed
	}
	for i, blob := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if i < len(paths) {
			hashed[paths[i]] = blob
		}
	}
	return hashed
}

func prefixWithin(root, dir string) string {
	absRoot, rootErr := filepath.EvalSymlinks(root)
	absDir, dirErr := filepath.EvalSymlinks(dir)
	rel, relErr := filepath.Rel(absRoot, absDir)
	if rootErr != nil || dirErr != nil || relErr != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	return filepath.ToSlash(rel) + "/"
}

func gitOutput(dir string, args ...string) (string, bool) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	return out.String(), err == nil
}
