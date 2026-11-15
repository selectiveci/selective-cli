package localrun

import (
	"bytes"
	"errors"
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

func takeSnapshot(dir string) (treeSnapshot, error) {
	toplevel, ok := gitOutput(dir, "rev-parse", "--show-toplevel")
	root := strings.TrimSuffix(toplevel, "\n")
	if !ok || root == "" {
		return treeSnapshot{}, errors.New("not a git checkout")
	}
	listing, ok := gitOutput(root, "ls-tree", "-r", "-z", "--full-tree", "HEAD")
	if !ok {
		return treeSnapshot{}, errors.New("not a git checkout")
	}
	sha, _ := gitOutput(root, "rev-parse", "HEAD")
	snapshot := treeSnapshot{SHA: strings.TrimSpace(sha), Prefix: prefixWithin(root, dir), Files: map[string]string{}}
	for _, entry := range strings.Split(listing, "\x00") {
		meta, path, found := strings.Cut(entry, "\t")
		if fields := strings.Split(meta, " "); found && len(fields) > 2 {
			snapshot.Files[path] = fields[2]
		}
	}
	var err error
	if snapshot.Dirty, err = foldWorkingTree(root, snapshot.Files); err != nil {
		return treeSnapshot{}, err
	}
	return snapshot, nil
}

func foldWorkingTree(root string, files map[string]string) ([]string, error) {
	status, ok := gitOutput(root, "status", "--porcelain=v1", "-z", "--untracked-files=no", "--no-renames")
	if !ok {
		return nil, errors.New("git status failed; cannot tell which files are dirty")
	}
	var changed, deleted, batched []string
	for _, entry := range strings.Split(status, "\x00") {
		if len(entry) < 4 {
			continue
		}
		path := entry[3:]
		info, statErr := os.Lstat(filepath.Join(root, path))
		if strings.Contains(entry[:2], "D") && (statErr != nil || info.IsDir()) {
			delete(files, path)
			deleted = append(deleted, path)
			continue
		}
		changed = append(changed, path)
		if statErr != nil {
			continue
		}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			if target, err := os.Readlink(filepath.Join(root, path)); err == nil {
				hashInto(files, path, root, target, "--stdin")
			}
		case info.Mode().IsRegular() && strings.Contains(path, "\n"):
			hashInto(files, path, root, "", "--", path)
		case info.Mode().IsRegular():
			batched = append(batched, path)
		}
	}
	for path, blob := range hashObjects(root, batched) {
		files[path] = blob
	}
	return append(append([]string{}, changed...), deleted...), nil
}

func hashInto(files map[string]string, path, root, stdin string, args ...string) {
	cmd := exec.Command("git", append([]string{"-C", root, "hash-object"}, args...)...)
	cmd.Stdin = strings.NewReader(stdin)
	if out, err := cmd.Output(); err == nil {
		files[path] = strings.TrimSpace(string(out))
	}
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
