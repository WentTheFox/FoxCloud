package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
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
  foxcloud conflicts resolve [job]               go through the conflicts one by one, asking what to keep

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
		case "diff", "keep", "clean", "resolve":
			sub, args = args[0], args[1:]
		case "-h", "-help", "--help", "help":
			fmt.Println(conflictsUsage)
			return nil
		}
	}
	switch sub {
	case "", "clean", "resolve":
		if len(args) > 1 {
			return errors.New(conflictsUsage)
		}
		only := ""
		if len(args) == 1 {
			only = args[0]
		}
		switch sub {
		case "":
			return listConflicts(cfg, only)
		case "clean":
			return cleanConflicts(cfg, only)
		}
		return resolveConflicts(cfg, only, os.Stdin, os.Stdout)
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
			return diffConflict(os.Stdout, r.Root, c)
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
func diffConflict(w io.Writer, root string, c conflict.Conflict) error {
	if !c.Exists {
		fmt.Fprintf(w, "%s no longer exists under its own name; %d conflict copy(ies) remain.\n", c.Original.Path, len(c.Variants))
		return nil
	}
	orig := filepath.Join(root, filepath.FromSlash(c.Original.Path))
	for _, v := range c.Variants {
		copyPath := filepath.Join(root, filepath.FromSlash(v.Path))
		fmt.Fprintf(w, "== %s (%s, %s) vs .conflict%d (%s, %s)\n",
			c.Original.Path, byteSize(c.Original.Size), c.Original.ModTime.Local().Format("2006-01-02 15:04"),
			v.N, byteSize(v.Size), v.ModTime.Local().Format("2006-01-02 15:04"))
		if same, err := conflict.SameContent(orig, copyPath); err == nil && same {
			fmt.Fprintln(w, "identical content (`foxcloud conflicts clean` archives it)")
			continue
		}
		if !looksLikeText(orig) || !looksLikeText(copyPath) || runtime.GOOS == "windows" {
			fmt.Fprintln(w, "content differs (not shown: binary, large, or no diff tool here)")
			continue
		}
		cmd := exec.Command("diff", "-u", "--label", c.Original.Path, "--label", v.Path, orig, copyPath)
		cmd.Stdout, cmd.Stderr = w, w
		var ee *exec.ExitError
		if err := cmd.Run(); err != nil && !(errors.As(err, &ee) && ee.ExitCode() == 1) {
			fmt.Fprintln(w, "content differs (diff is not available:", err, ")")
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

// resolveConflicts walks through every conflict of the job (or of all jobs) and
// asks what to keep. It is meant for a terminal the widget opens, so it says
// what it is doing and waits before returning, letting the window be read.
func resolveConflicts(cfg *config.Config, only string, in io.Reader, out io.Writer) error {
	r := bufio.NewReader(in)
	ask := func(prompt string) (string, error) {
		fmt.Fprint(out, prompt)
		line, err := r.ReadString('\n')
		if err != nil && line == "" {
			return "", err
		}
		return strings.ToLower(strings.TrimSpace(line)), nil
	}

	names := syncedJobs(cfg, only)
	if only != "" && len(names) == 0 {
		return fmt.Errorf("no job named %q with a local folder", only)
	}
	handled, left := 0, 0
loop:
	for _, name := range names {
		res, inc, err := conflictJob(cfg, name)
		if err != nil {
			return err
		}
		cs, err := conflict.Scan(res.Root, inc)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		for i, c := range cs {
			fmt.Fprintf(out, "\n[%s %d/%d] %s\n", name, i+1, len(cs), c.Original.Path)
			printVersions(out, res.Root, c)
			for {
				ans, err := ask("Keep: (o)riginal, (b)oth, copy number" + diffHint(c) + ", (s)kip, (q)uit > ")
				if err != nil {
					left += len(cs) - i
					break loop
				}
				var done []string
				var aerr error
				switch {
				case ans == "q":
					left += len(cs) - i
					break loop
				case ans == "s" || ans == "":
					left++
				case ans == "d":
					diffConflict(out, res.Root, c)
					continue
				case ans == "o":
					done, aerr = res.KeepOriginal(c)
				case ans == "b":
					done, aerr = res.KeepBoth(c)
				default:
					n, perr := strconv.Atoi(ans)
					if perr != nil {
						fmt.Fprintln(out, "Not an option.")
						continue
					}
					done, aerr = res.KeepVariant(c, n)
				}
				for _, line := range done {
					fmt.Fprintln(out, " ", line)
				}
				if aerr != nil {
					fmt.Fprintln(out, "Could not do that:", aerr)
					continue
				}
				if len(done) > 0 {
					handled++
				}
				break
			}
		}
	}
	switch {
	case handled == 0 && left == 0:
		fmt.Fprintln(out, "No conflicts.")
	default:
		fmt.Fprintf(out, "\nResolved %d, left %d. Displaced files are in each job's backup folder under conflicts/.\n", handled, left)
	}
	ask("Press Enter to close. ")
	return nil
}

func diffHint(c conflict.Conflict) string {
	if c.Exists {
		return ", (d)iff"
	}
	return ""
}

func printVersions(out io.Writer, root string, c conflict.Conflict) {
	orig := filepath.Join(root, filepath.FromSlash(c.Original.Path))
	if c.Exists {
		fmt.Fprintf(out, "  original  %s, %s\n", byteSize(c.Original.Size), c.Original.ModTime.Local().Format("2006-01-02 15:04"))
	} else {
		fmt.Fprintln(out, "  original  missing (only conflict copies remain)")
	}
	for _, v := range c.Variants {
		note := ""
		if c.Exists {
			if eq, _ := conflict.SameContent(orig, filepath.Join(root, filepath.FromSlash(v.Path))); eq {
				note = "  (identical to the original)"
			}
		}
		fmt.Fprintf(out, "  copy %d    %s, %s%s\n", v.N, byteSize(v.Size), v.ModTime.Local().Format("2006-01-02 15:04"), note)
	}
}
