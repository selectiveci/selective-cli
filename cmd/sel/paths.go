package main

import (
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/selectiveci/selective-cli/internal/contract"
)

func candidatePaths(root string, args []string) ([]string, error) {
	var paths []string
	for _, arg := range args {
		abs := arg
		if !filepath.IsAbs(arg) {
			abs = filepath.Join(root, arg)
		}
		rel, err := relativeTo(os.DirFS("/"), root, abs)
		if err != nil || !within(rel) {
			return nil, contract.Errorf(contract.UsageError, "%s is outside the current directory; pass paths under it", arg)
		}
		if _, err := os.Stat(abs); err != nil {
			return nil, contract.Errorf(contract.UsageError, "no such path: %s", arg)
		}
		paths = append(paths, filepath.ToSlash(rel))
	}
	return paths, nil
}

func relativeTo(fsys fs.FS, root, abs string) (string, error) {
	spell := func(p string) string {
		return filepath.FromSlash("/" + onDiskCase(fsys, strings.Trim(filepath.ToSlash(filepath.Clean(p)), "/")))
	}
	physical, err := filepath.Rel(spell(resolveLinks(root)), spell(resolveLinks(abs)))
	if err != nil || !within(physical) {
		return physical, err
	}
	if lexical, err := filepath.Rel(spell(root), spell(abs)); err == nil && within(lexical) {
		return lexical, nil
	}
	return physical, nil
}

func resolveLinks(p string) string {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return p
}

func within(rel string) bool {
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func onDiskCase(fsys fs.FS, rel string) string {
	if rel == "." {
		return rel
	}
	var spelled []string
	dir := "."
	for _, name := range strings.Split(rel, "/") {
		if other := swapCase(name); other != name {
			if _, err := fs.Stat(fsys, path.Join(dir, other)); err == nil {
				name = spellingIn(fsys, dir, name)
			}
		}
		spelled = append(spelled, name)
		dir = path.Join(dir, name)
	}
	return strings.Join(spelled, "/")
}

func swapCase(name string) string {
	if upper := strings.ToUpper(name); upper != name {
		return upper
	}
	return strings.ToLower(name)
}

func spellingIn(fsys fs.FS, dir, name string) string {
	entries, _ := fs.ReadDir(fsys, dir)
	if slices.ContainsFunc(entries, func(e fs.DirEntry) bool { return e.Name() == name }) {
		return name
	}
	if i := slices.IndexFunc(entries, func(e fs.DirEntry) bool { return strings.EqualFold(e.Name(), name) }); i >= 0 {
		return entries[i].Name()
	}
	return name
}
