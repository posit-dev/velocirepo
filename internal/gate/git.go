// Package gate runs the nightly fetch so that it only adds data: it checks
// the commits made since its last run, fetches, checks the night's changes,
// commits data and concatenation separately, pushes, and reports in a
// tracking issue.
package gate

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// checkedRef is the last commit the gate checked.
const checkedRef = "refs/velocirepo/checked"

// git runs Git in the data repository's work tree.
type git struct {
	dir string
	// env is added to Git's environment, such as the committer.
	env []string
}

func (g git) run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", g.dir}, args...)...)
	cmd.Env = append(cmd.Environ(), g.env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// ok runs Git and reports whether it succeeded, for questions such as "is
// this an ancestor".
func (g git) ok(ctx context.Context, args ...string) bool {
	_, err := g.run(ctx, args...)
	return err == nil
}

func (g git) head(ctx context.Context) (string, error) {
	return g.run(ctx, "rev-parse", "HEAD")
}

// resolve returns the commit a ref points to, or "" if it doesn't exist.
func (g git) resolve(ctx context.Context, ref string) string {
	out, err := g.run(ctx, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil {
		return ""
	}
	return out
}

func (g git) isAncestor(ctx context.Context, ancestor, of string) bool {
	return g.ok(ctx, "merge-base", "--is-ancestor", ancestor, of)
}

// commitsSince returns the commits after base up to head, oldest first,
// following the first parent, as the branch's history.
func (g git) commitsSince(ctx context.Context, base, head string) ([]string, error) {
	out, err := g.run(ctx, "rev-list", "--reverse", "--first-parent", base+".."+head)
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

// dirty reports whether the work tree has changes under the paths.
func (g git) dirty(ctx context.Context, paths ...string) (bool, error) {
	out, err := g.run(ctx, append([]string{"status", "--porcelain", "--untracked-files=all", "--"}, paths...)...)
	return out != "", err
}

// commitPaths commits everything under the paths, and returns the commit,
// or "" if there was nothing to commit.
func (g git) commitPaths(ctx context.Context, message string, paths ...string) (string, error) {
	if _, err := g.run(ctx, append([]string{"add", "--all", "--"}, paths...)...); err != nil {
		return "", err
	}
	if g.ok(ctx, "diff", "--cached", "--quiet") {
		return "", nil
	}
	if _, err := g.run(ctx, "commit", "--quiet", "--no-verify", "-m", message); err != nil {
		return "", err
	}
	return g.head(ctx)
}

func (g git) hasRemote(ctx context.Context, remote string) bool {
	return g.ok(ctx, "remote", "get-url", remote)
}

func (g git) branch(ctx context.Context) (string, error) {
	return g.run(ctx, "symbolic-ref", "--short", "HEAD")
}
