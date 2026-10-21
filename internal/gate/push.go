package gate

import (
	"context"
	"fmt"

	"github.com/posit-dev/velocirepo/internal/check"
)

// pushAttempts is how often a push is tried when others keep pushing.
const pushAttempts = 3

// push pushes HEAD and moves the checked ref with it, atomically, so the ref
// never skips an unchecked commit. base is the commit the run started from.
// When someone pushed during the run, their new commits are checked like any
// commits made by hand, the run's commits are rebased onto them, and the
// projects both touched are checked again; the findings are returned.
func (r *run) push(ctx context.Context, base string) ([]OpenFinding, error) {
	remote := r.remote()
	if !r.git.hasRemote(ctx, remote) {
		_, err := r.git.run(ctx, "update-ref", checkedRef, "HEAD")
		return nil, err
	}
	branch, err := r.git.branch(ctx)
	if err != nil {
		return nil, err
	}
	tracking := "refs/remotes/" + remote + "/" + branch

	var raced []OpenFinding
	for attempt := 1; ; attempt++ {
		_, err := r.git.run(ctx, "push", "--atomic", "--quiet", remote, "HEAD:refs/heads/"+branch, "+HEAD:"+checkedRef)
		if err == nil {
			_, err = r.git.run(ctx, "update-ref", checkedRef, "HEAD")
			return raced, err
		}
		if attempt == pushAttempts {
			return raced, fmt.Errorf("push: %w", err)
		}
		r.logf("The push was rejected; checking what was pushed in the meantime")
		if _, err := r.git.run(ctx, "fetch", "--quiet", remote, "+refs/heads/"+branch+":"+tracking); err != nil {
			return raced, err
		}
		upstream := r.git.resolve(ctx, tracking)
		if base == "" || !r.git.isAncestor(ctx, base, upstream) {
			if base, err = r.git.run(ctx, "merge-base", "HEAD", upstream); err != nil {
				return raced, err
			}
		}

		theirs, err := r.git.commitsSince(ctx, base, upstream)
		if err != nil {
			return raced, err
		}
		for _, c := range theirs {
			findings, err := r.diffCommit(ctx, c+"^", c, c)
			if err != nil {
				return raced, err
			}
			raced = append(raced, r.openFindings(findings, c)...)
		}
		touchedByThem, err := r.touched(ctx, base, upstream)
		if err != nil {
			return raced, err
		}
		touchedByUs, err := r.touched(ctx, base, "HEAD")
		if err != nil {
			return raced, err
		}

		if _, err := r.git.run(ctx, "rebase", "--quiet", upstream); err != nil {
			_, _ = r.git.run(ctx, "rebase", "--abort")
			return raced, fmt.Errorf("rebase onto what was pushed during the run: %w", err)
		}

		// Projects both touched are checked again, on the rebased result.
		both := map[string]bool{}
		for k := range touchedByUs {
			if touchedByThem[k] {
				both[k] = true
			}
		}
		if len(both) > 0 {
			diffs, repairs, err := check.CompareData(ctx, r.dataDir, check.Diff{Base: upstream, Head: "HEAD"})
			if err != nil {
				return raced, err
			}
			var again []check.ProjectDiff
			for _, d := range diffs {
				if both[d.Category+"/"+d.Source+"/"+d.Project] {
					again = append(again, d)
				}
			}
			raced = append(raced, r.openFindings(check.DiffFindings(r.cfg, again, repairs), "")...)
		}
		base = upstream
	}
}

// touched returns the projects whose data differs between two commits, as
// category/source/project.
func (r *run) touched(ctx context.Context, base, head string) (map[string]bool, error) {
	diffs, _, err := check.CompareData(ctx, r.dataDir, check.Diff{Base: base, Head: head})
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, d := range diffs {
		out[d.Category+"/"+d.Source+"/"+d.Project] = true
	}
	return out, nil
}
