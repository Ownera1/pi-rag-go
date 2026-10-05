package document

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// CanonicalFiles treats a source-manifest/MinerU directory as one document.
// Explicit manifests take precedence. Ambiguous multi-paper folders fail closed.
func CanonicalFiles(ctx context.Context, paths []string) ([]string, error) {
	manifests := map[string]string{}
	candidates := map[string][]string{}
	for _, p := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		dir := filepath.Dir(p)
		if filepath.Base(p) == "rag-source.json" {
			manifests[dir] = p
		} else if IsMinerUFile(p) {
			candidates[dir] = append(candidates[dir], p)
		}
	}
	chosen := map[string]string{}
	for dir, path := range manifests {
		chosen[dir] = path
	}
	dirs := make([]string, 0, len(candidates))
	for dir := range candidates {
		dirs = append(dirs, dir)
	}
	sort.Slice(dirs, func(i, j int) bool {
		if len(dirs[i]) == len(dirs[j]) {
			return dirs[i] < dirs[j]
		}
		return len(dirs[i]) < len(dirs[j])
	})
	for _, dir := range dirs {
		covered := false
		for parent := dir; ; parent = filepath.Dir(parent) {
			if _, ok := chosen[parent]; ok {
				covered = true
				break
			}
			if filepath.Dir(parent) == parent {
				break
			}
		}
		if covered {
			continue
		}
		files := candidates[dir]
		stems := map[string]bool{}
		for _, f := range files {
			name := strings.ToLower(filepath.Base(f))
			for _, suffix := range []string{"_content_list_v2.json", "_content_list.json", "_middle.json", ".mineru.json"} {
				if strings.HasSuffix(name, suffix) {
					name = strings.TrimSuffix(name, suffix)
					break
				}
			}
			if strings.Contains(name, ".json") {
				name = ""
			}
			if name != "" {
				stems[name] = true
			}
		}
		if len(stems) > 1 {
			return nil, fmt.Errorf("multiple MinerU documents in %s; use one directory per paper or rag-source.json", dir)
		}
		priority := func(p string) int {
			name := strings.ToLower(filepath.Base(p))
			if strings.HasSuffix(name, ".mineru.json") || name == "structured_content.json" {
				return 4
			}
			if strings.HasSuffix(name, "content_list.json") {
				return 3
			}
			if strings.HasSuffix(name, "content_list_v2.json") {
				return 1
			}
			return 2
		}
		sort.Slice(files, func(i, j int) bool {
			a, b := priority(files[i]), priority(files[j])
			if a == b {
				return files[i] < files[j]
			}
			return a > b
		})
		chosen[dir] = files[0]
	}
	out := []string{}
	seen := map[string]bool{}
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		selected := ""
		for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
			if p, ok := chosen[dir]; ok {
				selected = p
			}
			if filepath.Dir(dir) == dir {
				break
			}
		}
		if (selected != "" && path != selected) || seen[path] {
			continue
		}
		seen[path] = true
		out = append(out, path)
	}
	sort.Strings(out)
	return out, nil
}
