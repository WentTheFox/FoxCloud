package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/WentTheFox/FoxCloud/internal/config"
	"github.com/WentTheFox/FoxCloud/internal/conflict"
)

const conflictsUsage = `usage:
  foxcloud conflicts [job]                       list files that have bisync conflict copies
  foxcloud conflicts diff <job> <path>           compare a file with its conflict copies
  foxcloud conflicts keep <job> <path> <choice>  choice: original, both, or a copy's number (1 for .conflict1)
  foxcloud conflicts clean [job]                 archive conflict copies identical to their original

Nothing is deleted: displaced files go to <backup_dir>/conflicts/<time>/.`

// conflictJob returns the folder a job syncs, its filter and where displaced
// files are archived.
func conflictJob(cfg *config.Config, name string) (*conflict.Resolver, func(string) bool, error) {
	j, ok := cfg.Job(name)
	if !ok {
		return nil, nil, fmt.Errorf("no job named %q", name)
	}
	return conflict.ForJob(j)
}

func syncedJobs(cfg *config.Config, only string) []string {
	var names []string
	for _, j := range cfg.Jobs {
		if j.Role != config.RoleMount && j.LocalPath != "" && (only == "" || only == j.Name) {
			names = append(names, j.Name)
		}
	}
	return names
}

func conflictsCmd(cfg *config.Config, args []string) error {
	sub := ""
	if len(args) > 0 {
		switch args[0] {
		case "diff", "keep", "clean":
			sub, args = args[0], args[1:]
		case "-h", "-help", "--help", "help":
			fmt.Println(conflictsUsage)
			return nil
		}
	}
	switch sub {
	case "", "clean":
		if len(args) > 1 {
			return errors.New(conflictsUsage)
		}
		only := ""
		if len(args) == 1 {
			only = args[0]
		}
		if sub == "" {
			return listConflicts(cfg, only)
		}
		return cleanConflicts(cfg, only)
	default:
		want := 2
		if sub == "keep" {
			want = 3
		}
		if len(args) != want {
			return errors.New(conflictsUsage)
		}
		r, inc, err := conflictJob(cfg, args[0])
		if err != nil {
			return err
		}
		cs, err := conflict.Scan(r.Root, inc)
		if err != nil {
			return err
		}
		c, ok := conflict.Find(cs, args[1])
		if !ok {
			return fmt.Errorf("%s has no conflict copies (list them with `foxcloud conflicts %s`)", args[1], args[0])
		}
		if sub == "diff" {
			return diffConflict(r.Root, c)
		}
		var done []string
		switch choice := args[2]; choice {
		case "original":
			done, err = r.KeepOriginal(c)
		case "both":
			done, err = r.KeepBoth(c)
		default:
			n, perr := strconv.Atoi(choice)
			if perr != nil {
				return fmt.Errorf("choice must be original, both or a number, not %q", choice)
			}
			done, err = r.KeepVariant(c, n)
		}
		for _, line := range done {
			fmt.Println(line)
		}
		return err
	}
}

func cleanConflicts(cfg *config.Config, only string) error {
	for _, name := range syncedJobs(cfg, only) {
		r, inc, err := conflictJob(cfg, name)
		if err != nil {
			return err
		}
		cs, err := conflict.Scan(r.Root, inc)
		if err != nil {
			return err
		}
		done, err := r.Clean(cs)
		for _, line := range done {
			fmt.Println(line)
		}
		if err != nil {
			return err
		}
		fmt.Printf("%s: %d identical copy(ies) archived\n", name, len(done))
	}
	return nil
}

func listConflicts(cfg *config.Config, only string) error {
	total := 0
	for _, name := range syncedJobs(cfg, only) {
		r, inc, err := conflictJob(cfg, name)
		if err != nil {
			return err
		}
		cs, err := conflict.Scan(r.Root, inc)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		for _, c := range cs {
			total++
			if c.Exists {
				fmt.Printf("%s: %s  (%s, %s)\n", name, c.Original.Path, byteSize(c.Original.Size), c.Original.ModTime.Local().Format("2006-01-02 15:04"))
			} else {
				fmt.Printf("%s: %s  (missing; only conflict copies remain)\n", name, c.Original.Path)
			}
			for _, v := range c.Variants {
				note := ""
				if c.Exists {
					orig := filepath.Join(r.Root, filepath.FromSlash(c.Original.Path))
					if eq, _ := conflict.SameContent(orig, filepath.Join(r.Root, filepath.FromSlash(v.Path))); eq {
						note = ", identical to the original"
					}
				}
				fmt.Printf("    .conflict%d  %s, %s%s\n", v.N, byteSize(v.Size), v.ModTime.Local().Format("2006-01-02 15:04"), note)
			}
		}
	}
	if total == 0 {
		fmt.Println("No conflicts.")
	}
	return nil
}

func byteSize(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// diffConflict shows how each conflict copy differs from the original: the
// system's diff for small text files, otherwise a size and time comparison.
func diffConflict(root string, c conflict.Conflict) error {
	if !c.Exists {
		fmt.Printf("%s no longer exists under its own name; %d conflict copy(ies) remain.\n", c.Original.Path, len(c.Variants))
		return nil
	}
	orig := filepath.Join(root, filepath.FromSlash(c.Original.Path))
	for _, v := range c.Variants {
		copyPath := filepath.Join(root, filepath.FromSlash(v.Path))
		fmt.Printf("== %s (%s, %s) vs .conflict%d (%s, %s)\n",
			c.Original.Path, byteSize(c.Original.Size), c.Original.ModTime.Local().Format("2006-01-02 15:04"),
			v.N, byteSize(v.Size), v.ModTime.Local().Format("2006-01-02 15:04"))
		if same, err := conflict.SameContent(orig, copyPath); err == nil && same {
			fmt.Println("identical content (`foxcloud conflicts clean` archives it)")
			continue
		}
		if !looksLikeText(orig) || !looksLikeText(copyPath) || runtime.GOOS == "windows" {
			fmt.Println("content differs (not shown: binary, large, or no diff tool here)")
			continue
		}
		cmd := exec.Command("diff", "-u", "--label", c.Original.Path, "--label", v.Path, orig, copyPath)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		var ee *exec.ExitError
		if err := cmd.Run(); err != nil && !(errors.As(err, &ee) && ee.ExitCode() == 1) {
			fmt.Println("content differs (diff is not available:", err, ")")
		}
	}
	return nil
}

// looksLikeText is true for a file under 1 MiB with no NUL byte in its first 8 KiB.
func looksLikeText(p string) bool {
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || fi.Size() > 1<<20 {
		return false
	}
	buf := make([]byte, 8<<10)
	n, _ := f.Read(buf)
	return !strings.Contains(string(buf[:n]), "\x00")
}
