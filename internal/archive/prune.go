package archive

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// PruneResult reports what Prune did.
type PruneResult struct {
	Files   int   // checkpoint files examined
	Removed int   // files deleted
	Before  int64 // bytes before
	After   int64 // bytes after
}

// Prune deletes the oldest cached checkpoint files (by modification time)
// until the cache holds at most maxBytes. Only *.xdr.gz files are touched.
func Prune(dir string, maxBytes int64) (PruneResult, error) {
	type file struct {
		path string
		size int64
		mod  int64
	}
	var files []file
	var res PruneResult
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".xdr.gz") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		files = append(files, file{path, info.Size(), info.ModTime().UnixNano()})
		res.Before += info.Size()
		return nil
	})
	if err != nil {
		return res, err
	}
	res.Files = len(files)
	sort.Slice(files, func(i, j int) bool { return files[i].mod < files[j].mod })
	res.After = res.Before
	for _, f := range files {
		if res.After <= maxBytes {
			break
		}
		if err := os.Remove(f.path); err != nil {
			return res, err
		}
		res.After -= f.size
		res.Removed++
	}
	return res, nil
}
