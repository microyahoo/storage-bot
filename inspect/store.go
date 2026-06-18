package inspect

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

type Store struct {
	dir  string
	keep int
}

func NewStore(dir string, keep int) *Store {
	if keep <= 0 {
		keep = 30
	}
	return &Store{dir: dir, keep: keep}
}

func (s *Store) clusterDir(cluster string) string {
	return filepath.Join(s.dir, cluster)
}

// Save writes the report as <dir>/<cluster>/<timestamp>.json then prunes old ones.
func (s *Store) Save(r *Report) error {
	cd := s.clusterDir(r.Cluster)
	if err := os.MkdirAll(cd, 0o755); err != nil {
		return fmt.Errorf("mkdir history: %w", err)
	}
	name := r.StartedAt.UTC().Format("20060102-150405") + ".json"
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal report: %w", err)
	}
	if err := os.WriteFile(filepath.Join(cd, name), data, 0o644); err != nil {
		return fmt.Errorf("write report: %w", err)
	}
	return s.prune(r.Cluster)
}

// List returns report filenames for a cluster, newest first.
func (s *Store) List(cluster string) ([]string, error) {
	entries, err := os.ReadDir(s.clusterDir(cluster))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".json" {
			names = append(names, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	return names, nil
}

func (s *Store) Load(cluster, name string) (*Report, error) {
	data, err := os.ReadFile(filepath.Join(s.clusterDir(cluster), name))
	if err != nil {
		return nil, err
	}
	var r Report
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("unmarshal report: %w", err)
	}
	return &r, nil
}

func (s *Store) prune(cluster string) error {
	names, err := s.List(cluster)
	if err != nil {
		return err
	}
	for _, old := range names[min(len(names), s.keep):] {
		_ = os.Remove(filepath.Join(s.clusterDir(cluster), old))
	}
	return nil
}
