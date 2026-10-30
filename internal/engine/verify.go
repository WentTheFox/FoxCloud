package engine

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"strings"

	"github.com/WentTheFox/FoxCloud/internal/config"
	"github.com/WentTheFox/FoxCloud/internal/rclone"
)

// VerifyResult describes a round-trip check.
type VerifyResult struct {
	// Sampled is how many files were downloaded, decrypted and compared.
	Sampled int
	// Candidates is how many files were eligible to be picked.
	Candidates int
}

// Verify downloads a random sample of a job's files from the remote, decrypts
// them and compares the bytes with the local originals. Sizes and modtimes are
// not trusted: crypt has no plaintext hashes, so only a real download proves the
// data on the server decrypts back to what is on disk.
func (e *Engine) Verify(ctx context.Context, name string, n int, maxSize string) (VerifyResult, error) {
	var res VerifyResult
	job, ok := e.Cfg.Job(name)
	if !ok {
		return res, fmt.Errorf("no job named %q", name)
	}
	if job.Role == config.RoleMount {
		return res, fmt.Errorf("job %q is a mount job and has no local folder to compare", name)
	}
	if n < 1 {
		return res, errors.New("sample size must be at least 1")
	}

	args := []string{"lsf", "-R", "--files-only"}
	if job.FilterFile != "" {
		args = append(args, "--filter-from", job.FilterFile)
	}
	if maxSize != "" {
		args = append(args, "--max-size", maxSize)
	}
	out, err := rclone.Output(ctx, e.Rclone, append(args, job.LocalPath), nil)
	if err != nil {
		return res, err
	}
	var candidates []string
	for _, l := range strings.Split(string(out), "\n") {
		if l = strings.TrimRight(l, "\r"); l != "" {
			candidates = append(candidates, l)
		}
	}
	res.Candidates = len(candidates)
	picked := pickSample(candidates, n, rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64())))
	if len(picked) == 0 {
		return res, errors.New("no files to verify")
	}
	res.Sampled = len(picked)

	list, err := os.CreateTemp("", "foxcloud-verify-*.txt")
	if err != nil {
		return res, err
	}
	defer os.Remove(list.Name())
	_, werr := list.WriteString(strings.Join(picked, "\n") + "\n")
	if cerr := list.Close(); werr != nil || cerr != nil {
		return res, errors.Join(werr, cerr)
	}

	env, err := e.remoteEnv(ctx)
	if err != nil {
		return res, err
	}
	// --files-from-raw takes names exactly as listed, so '#' or leading spaces in
	// a file name are not mistaken for comments or trimmed away.
	checkArgs := []string{"check", job.LocalPath, rclone.CryptRemote(job.RemotePath),
		"--download", "--one-way", "--files-from-raw", list.Name(), "-v"}
	if _, err := rclone.Run(ctx, e.Rclone, checkArgs, env, e.Out); err != nil {
		return res, fmt.Errorf("verification found differences or errors: %w", err)
	}
	return res, nil
}

// pickSample returns up to n distinct entries of paths in random order.
func pickSample(paths []string, n int, r *rand.Rand) []string {
	cp := append([]string(nil), paths...)
	r.Shuffle(len(cp), func(i, j int) { cp[i], cp[j] = cp[j], cp[i] })
	if n < len(cp) {
		cp = cp[:n]
	}
	return cp
}
