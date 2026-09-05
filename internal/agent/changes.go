package agent

import (
	"quecto-harness/internal/diff"
	"quecto-harness/internal/store"
	"quecto-harness/internal/tools"
)

// diffLimit bounds the lines carried in one file's hunks. A rewritten file
// still reports its true counts; what the limit cuts is only the body.
const diffLimit = 600

// What a file changed into. Only changed and new carry hunks; the rest name
// why there is nothing to draw.
const (
	StatusChanged = "changed"
	StatusNew     = "new"
	StatusDeleted = "deleted"
	StatusSame    = "same"
	StatusBinary  = "binary"
	StatusLarge   = "large"
	// The copy kept before the first write is no longer on disk.
	StatusLost = "lost"
)

// FileChange is one file this session has written to, compared against the
// copy kept before its first write.
type FileChange struct {
	Path    string      `json:"path"`
	Status  string      `json:"status"`
	Added   int         `json:"added"`
	Removed int         `json:"removed"`
	Omitted int         `json:"omitted"`
	Hunks   []diff.Hunk `json:"hunks"`
}

// Changes compares every file the session wrote against its baseline and the
// file on disk now, so an edit made outside the harness shows up too.
func (e *Engine) Changes(id string) ([]FileChange, error) {
	s, err := e.Get(id)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	project := s.Project
	baselines := append([]store.Baseline(nil), s.Baselines...)
	s.mu.Unlock()

	out := make([]FileChange, 0, len(baselines))
	if len(baselines) == 0 {
		return out, nil
	}
	runner, err := tools.NewRunner(project, e.cfg.Config())
	if err != nil {
		return nil, err
	}
	for _, b := range baselines {
		out = append(out, e.compare(id, runner, b))
	}
	return out, nil
}

// compare builds one row of the change list.
func (e *Engine) compare(id string, runner *tools.Runner, b store.Baseline) FileChange {
	row := FileChange{Path: b.Path, Hunks: []diff.Hunk{}}

	now, err := runner.Current(b.Path)
	if err != nil {
		// The path no longer resolves inside the project, so there is nothing
		// to read and nothing honest to say about it.
		row.Status = StatusDeleted
		return row
	}
	switch now.State {
	case tools.SnapshotNew:
		row.Status = StatusDeleted
		return row
	case tools.SnapshotBinary:
		row.Status = StatusBinary
		return row
	case tools.SnapshotLarge:
		row.Status = StatusLarge
		return row
	}

	// The file reads now, but a comparison also needs what it was.
	switch b.State {
	case store.BaselineBinary:
		row.Status = StatusBinary
		return row
	case store.BaselineLarge:
		row.Status = StatusLarge
		return row
	}

	old := ""
	if b.State == store.BaselineText && b.Blob != "" {
		if data, err := e.store.Blob(id, b.Blob); err == nil {
			old = string(data)
		} else {
			row.Status = StatusLost
			return row
		}
	}

	res := diff.Compute(old, string(now.Content), diffLimit)
	row.Added, row.Removed, row.Omitted = res.Added, res.Removed, res.Omitted
	row.Hunks = res.Hunks
	switch {
	case b.State == store.BaselineNew:
		row.Status = StatusNew
	case res.Identical:
		row.Status = StatusSame
	default:
		row.Status = StatusChanged
	}
	return row
}
