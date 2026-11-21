package localrun

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/selectiveci/selective-cli/internal/gitctx"
)

type treeSnapshot struct {
	SHA    string            `json:"sha"`
	Prefix string            `json:"prefix"`
	Files  map[string]string `json:"files"`
	Dirty  []string          `json:"dirty"`
	modes  map[string]string
}

func takeSnapshot(ctx context.Context, dir string) (treeSnapshot, error) {
	toplevel, ok := gitOutput(ctx, dir, "rev-parse", "--show-toplevel")
	root := strings.TrimSuffix(toplevel, "\n")
	if !ok || root == "" {
		return treeSnapshot{}, errors.New("not a git checkout")
	}
	listing, ok := gitOutput(ctx, root, "ls-tree", "-r", "-z", "--full-tree", "HEAD")
	if !ok {
		return treeSnapshot{}, errors.New("not a git checkout")
	}
	sha, _ := gitOutput(ctx, root, "rev-parse", "HEAD")
	snapshot := treeSnapshot{SHA: strings.TrimSpace(sha), Prefix: prefixWithin(root, dir), Files: map[string]string{}, modes: map[string]string{}}
	for _, entry := range strings.Split(listing, "\x00") {
		meta, path, found := strings.Cut(entry, "\t")
		if fields := strings.Split(meta, " "); found && len(fields) > 2 {
			snapshot.Files[path] = fields[2]
			snapshot.modes[path] = fields[0]
		}
	}
	var err error
	if snapshot.Dirty, err = foldWorkingTree(ctx, root, snapshot.Files, snapshot.modes); err != nil {
		return treeSnapshot{}, err
	}
	return snapshot, nil
}

func foldWorkingTree(ctx context.Context, root string, files, modes map[string]string) ([]string, error) {
	status, ok := gitOutput(ctx, root, "status", "--porcelain=v1", "-z", "--untracked-files=no", "--no-renames")
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
		modes[path] = gitMode(info)
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			if target, err := os.Readlink(filepath.Join(root, path)); err == nil {
				hashInto(ctx, files, path, root, target, "--stdin")
			}
		case info.Mode().IsRegular() && strings.Contains(path, "\n"):
			hashInto(ctx, files, path, root, "", "--", path)
		case info.Mode().IsRegular():
			batched = append(batched, path)
		}
	}
	for path, blob := range hashObjects(ctx, root, batched) {
		files[path] = blob
	}
	return append(append([]string{}, changed...), deleted...), nil
}

func gitMode(info os.FileInfo) string {
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		return "120000"
	case info.Mode()&0o111 != 0:
		return "100755"
	}
	return "100644"
}

func hashInto(ctx context.Context, files map[string]string, path, root, stdin string, args ...string) {
	cmd := gitctx.Command(ctx, append([]string{"-C", root, "hash-object"}, args...)...)
	cmd.Stdin = strings.NewReader(stdin)
	if out, err := gitctx.Output(cmd); err == nil {
		files[path] = strings.TrimSpace(string(out))
	}
}

func hashObjects(ctx context.Context, root string, paths []string) map[string]string {
	hashed := map[string]string{}
	if len(paths) == 0 {
		return hashed
	}
	cmd := gitctx.Command(ctx, "-C", root, "hash-object", "--stdin-paths")
	cmd.Stdin = strings.NewReader(strings.Join(paths, "\n") + "\n")
	out, err := gitctx.Output(cmd)
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

func gitOutput(ctx context.Context, dir string, args ...string) (string, bool) {
	cmd := gitctx.Command(ctx, append([]string{"-C", dir}, args...)...)
	var out bytes.Buffer
	cmd.Stdout = &out
	err := gitctx.Run(cmd)
	return out.String(), err == nil
}
