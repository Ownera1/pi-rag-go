package rag

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Ownera1/rag-go/internal/document"
	"github.com/Ownera1/rag-go/internal/workspace"
)

type savedState struct {
	Inputs               map[string]string     `json:"inputs"`
	FailedFiles          []FileFailure         `json:"failedFiles"`
	LastSync             *IndexResult          `json:"lastSync,omitempty"`
	LastAttemptAt        time.Time             `json:"lastAttemptAt,omitempty"`
	LastAttemptSignature string                `json:"lastAttemptSignature,omitempty"`
	Fingerprints         map[string]inputStamp `json:"fingerprints,omitempty"`
}

// inputStamp caches an input fingerprint against file metadata so unchanged
// sources are not reread on every operation.
type inputStamp struct {
	Stamp string `json:"stamp"`
	Hash  string `json:"hash"`
}

// A file modified this close to hashing may change again within the same
// timestamp granularity, so its stamp is not trusted (the "racy" case).
const racyWindow = 3 * time.Second

func loadState(root string) (savedState, error) {
	s := savedState{Inputs: map[string]string{}, FailedFiles: []FileFailure{}}
	b, err := os.ReadFile(filepath.Join(root, "state.json"))
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	err = json.Unmarshal(b, &s)
	if s.Inputs == nil {
		s.Inputs = map[string]string{}
	}
	if s.FailedFiles == nil {
		s.FailedFiles = []FileFailure{}
	}
	return s, err
}

func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func addFailure(r *IndexResult, path, stage string, err error) {
	r.Failed++
	r.Errors = append(r.Errors, path+": "+err.Error())
	r.Failures = append(r.Failures, FileFailure{Path: path, Stage: stage, Error: err.Error()})
}

type sourceSnapshot struct {
	inputs    map[string]string
	paths     []string
	signature string
	failures  []FileFailure
}

func (s *session) snapshot(ctx context.Context) sourceSnapshot {
	snap := sourceSnapshot{inputs: map[string]string{}}
	paths, err := scan(ctx, s.docs, s.cfg.ExcludePatterns, s.root)
	if err != nil {
		snap.failures = []FileFailure{{Path: s.docs, Stage: "scan", Error: err.Error()}}
	}
	snap.paths = paths
	parts := []string{s.docs}
	stamps := map[string]inputStamp{}
	for _, path := range paths {
		hash, e := s.fingerprint(ctx, path, stamps)
		parts = append(parts, path+"\x00"+hash)
		if e != nil {
			snap.failures = append(snap.failures, FileFailure{Path: path, Stage: "scan", Error: e.Error()})
			parts = append(parts, e.Error())
		} else {
			snap.inputs[path] = hash
		}
	}
	if err == nil && ctx.Err() == nil {
		s.stamps = stamps
	}
	for _, f := range snap.failures {
		parts = append(parts, f.Path+"\x00"+f.Error)
	}
	snap.signature = document.ShortHash(strings.Join(parts, "\x00"))
	return snap
}

// fingerprint reuses a cached hash while the stamp is unchanged. The stamp is
// taken before reading, so a write during hashing yields a different stamp.
func (s *session) fingerprint(ctx context.Context, path string, stamps map[string]inputStamp) (string, error) {
	stamp, modified, err := document.InputStamp(path)
	if err != nil {
		return document.InputFingerprint(ctx, path)
	}
	if cached, ok := s.stamps[path]; ok && cached.Stamp == stamp {
		stamps[path] = cached
		return cached.Hash, nil
	}
	started := time.Now()
	hash, err := document.InputFingerprint(ctx, path)
	if err == nil && started.Sub(modified) > racyWindow {
		stamps[path] = inputStamp{Stamp: stamp, Hash: hash}
	}
	return hash, err
}

func (s *session) needsSync(snap sourceSnapshot) bool {
	if len(snap.failures) > 0 || len(s.state.FailedFiles) > 0 || len(snap.inputs) != len(s.state.Inputs) {
		return true
	}
	for path, hash := range snap.inputs {
		if s.state.Inputs[path] != hash {
			return true
		}
	}
	return s.db == nil
}

func (s *session) record(snap sourceSnapshot, r IndexResult) error {
	inputs := map[string]string{}
	if s.db != nil {
		var err error
		if inputs, err = s.db.Embedded(context.Background()); err != nil {
			return err
		}
	}
	sort.Slice(r.Failures, func(i, j int) bool { return r.Failures[i].Path < r.Failures[j].Path })
	s.state = savedState{Inputs: inputs, FailedFiles: r.Failures, LastSync: &r, LastAttemptAt: time.Now(), LastAttemptSignature: snap.signature, Fingerprints: s.stamps}
	return workspace.AtomicJSON(filepath.Join(s.root, "state.json"), s.state)
}
