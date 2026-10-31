package conflict

import (
	"fmt"
	"path/filepath"

	"github.com/WentTheFox/FoxCloud/internal/config"
	"github.com/WentTheFox/FoxCloud/internal/filter"
)

// ForJob returns a resolver for the folder a job syncs, with the job's filter
// as the include function for Scan. Displaced files are archived under the
// job's backup folder.
func ForJob(j config.Job) (*Resolver, func(string) bool, error) {
	if j.Role == config.RoleMount || j.LocalPath == "" {
		return nil, nil, fmt.Errorf("job %q has no local folder to check", j.Name)
	}
	var include func(string) bool
	if j.FilterFile != "" {
		m, err := filter.Load(j.FilterFile)
		if err != nil {
			return nil, nil, err
		}
		include = m.Include
	}
	return &Resolver{Root: j.LocalPath, Archive: filepath.Join(j.BackupDir, "conflicts")}, include, nil
}

// Count is how many files in the job's folder have conflict copies; a folder
// that cannot be read counts as none.
func Count(j config.Job) int {
	r, include, err := ForJob(j)
	if err != nil {
		return 0
	}
	cs, err := Scan(r.Root, include)
	if err != nil {
		return 0
	}
	return len(cs)
}
