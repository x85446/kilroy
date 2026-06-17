package engine

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type tarFilter func(relPath string, entry fs.DirEntry) bool

func isSensitiveCodexStatePath(relPath string) bool {
	rel := filepath.ToSlash(strings.TrimPrefix(relPath, "./"))
	if rel == "" || rel == "." {
		return false
	}
	parts := strings.Split(rel, "/")
	for _, part := range parts {
		if part == "codex-home" || strings.HasPrefix(part, "codex-home-retry") {
			return true
		}
	}
	// Defense-in-depth in case sensitive codex files are copied outside codex-home.
	for i := 0; i+1 < len(parts); i++ {
		if parts[i] != ".codex" {
			continue
		}
		if parts[i+1] == "auth.json" || parts[i+1] == "config.toml" {
			return true
		}
	}
	return false
}

func includeInStageArchive(rel string, _ fs.DirEntry) bool {
	if rel == "stage.tgz" || rel == "stage.tgz.tmp" {
		return false
	}
	if isSensitiveCodexStatePath(rel) {
		return false
	}
	return true
}

// includeInStageArchiveFlat is the variant used when RunOptions.NoStageArchiveStacking
// is set. It additionally excludes any nested visit_*/stage.tgz (and .tmp) files so
// that re-entries of the same node do not produce archives that recursively contain
// every prior visit's archive (issue #89). The per-visit metadata files (response.md,
// status.json, prompt.md, events.{json,ndjson}, stdout.log, etc.) under each visit_N/
// dir are still included — only the redundant inner tarballs are dropped.
func includeInStageArchiveFlat(rel string, d fs.DirEntry) bool {
	if !includeInStageArchive(rel, d) {
		return false
	}
	if strings.HasSuffix(rel, "/stage.tgz") || strings.HasSuffix(rel, "/stage.tgz.tmp") {
		return false
	}
	return true
}

func includeInRunArchive(rel string, _ fs.DirEntry) bool {
	if rel == "run.tgz" || rel == "run.tgz.tmp" {
		return false
	}
	if rel == "worktree" || strings.HasPrefix(rel, "worktree/") {
		return false
	}
	if isSensitiveCodexStatePath(rel) {
		return false
	}
	return true
}

func writeTarGz(dstPath string, srcDir string, include tarFilter) error {
	srcDir = filepath.Clean(srcDir)
	if srcDir == "." || srcDir == string(filepath.Separator) {
		return fmt.Errorf("refusing to tar root dir: %s", srcDir)
	}
	if include == nil {
		include = func(string, fs.DirEntry) bool { return true }
	}

	var files []string
	err := filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == srcDir {
			return nil
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == "." || rel == "" {
			return nil
		}
		if !include(rel, d) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		return err
	}
	sort.SliceStable(files, func(i, j int) bool { return files[i] < files[j] })

	if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
		return err
	}
	tmp := dstPath + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	gz := gzip.NewWriter(f)
	defer func() { _ = gz.Close() }()
	tw := tar.NewWriter(gz)
	defer func() { _ = tw.Close() }()

	for _, path := range files {
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		rel = strings.TrimPrefix(rel, "./")
		if rel == "" || rel == "." {
			continue
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		link := ""
		if info.Mode()&os.ModeSymlink != 0 {
			link, _ = os.Readlink(path)
		}
		hdr, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		hdr.Name = rel
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			r, err := os.Open(path)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(tw, r)
			_ = r.Close()
			if copyErr != nil {
				return copyErr
			}
		}
	}

	if err := tw.Close(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dstPath)
}
