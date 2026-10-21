package check

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
)

// gitRepo runs Git in a work tree.
type gitRepo struct {
	dir string
}

func (g gitRepo) run(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", g.dir}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// lines splits NUL-separated output.
func nulFields(out []byte) []string {
	var fields []string
	for _, f := range bytes.Split(out, []byte{0}) {
		if len(f) > 0 {
			fields = append(fields, string(f))
		}
	}
	return fields
}

// topLevel returns the work tree root of the repository containing dir.
func topLevel(ctx context.Context, dir string) (string, error) {
	out, err := gitRepo{dir: dir}.run(ctx, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// HasCommit reports whether dir is in a Git repository that has rev.
func HasCommit(ctx context.Context, dir, rev string) bool {
	_, err := gitRepo{dir: dir}.run(ctx, "rev-parse", "--verify", "--quiet", rev+"^{commit}")
	return err == nil
}

// changedFiles returns the files under prefix that differ between base and
// head, or between base and the work tree (untracked files included) when
// head is "".
func (g gitRepo) changedFiles(ctx context.Context, base, head, prefix string) ([]string, error) {
	args := []string{"diff", "--no-renames", "--name-only", "-z", base}
	if head != "" {
		args = append(args, head)
	}
	out, err := g.run(ctx, append(args, "--", prefix)...)
	if err != nil {
		return nil, err
	}
	files := nulFields(out)
	if head == "" {
		out, err := g.run(ctx, "ls-files", "--others", "--exclude-standard", "-z", "--", prefix)
		if err != nil {
			return nil, err
		}
		files = append(files, nulFields(out)...)
	}
	return files, nil
}

// listFiles returns the files under dir at a commit.
func (g gitRepo) listFiles(ctx context.Context, rev, dir string) ([]string, error) {
	out, err := g.run(ctx, "ls-tree", "-r", "-z", "--name-only", rev, "--", dir+"/")
	if err != nil {
		return nil, err
	}
	return nulFields(out), nil
}

// message returns the messages of the commits in base..head.
func (g gitRepo) messages(ctx context.Context, base, head string) (string, error) {
	out, err := g.run(ctx, "log", "--format=%B", base+".."+head)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// catFile reads blobs with one git cat-file --batch process.
type catFile struct {
	cmd *exec.Cmd
	in  io.WriteCloser
	out *bufio.Reader
}

func (g gitRepo) catFile(ctx context.Context) (*catFile, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", g.dir, "cat-file", "--batch")
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("git cat-file: %w", err)
	}
	return &catFile{cmd: cmd, in: in, out: bufio.NewReaderSize(out, 1<<20)}, nil
}

// onRead, if set, is called for every file the diff checks read, in tests.
var onRead func(path string)

// read returns a file's contents at a commit, or nil if it doesn't exist
// there.
func (c *catFile) read(rev, path string) ([]byte, error) {
	if onRead != nil {
		onRead(path)
	}
	if _, err := fmt.Fprintf(c.in, "%s:%s\n", rev, path); err != nil {
		return nil, err
	}
	header, err := c.out.ReadString('\n')
	if err != nil {
		return nil, fmt.Errorf("git cat-file: %w", err)
	}
	fields := strings.Fields(header)
	if len(fields) == 2 && fields[1] == "missing" {
		return nil, nil
	}
	if len(fields) != 3 {
		return nil, fmt.Errorf("git cat-file: unexpected header %q", header)
	}
	size, err := strconv.Atoi(fields[2])
	if err != nil {
		return nil, fmt.Errorf("git cat-file: unexpected header %q", header)
	}
	body := make([]byte, size+1)
	if _, err := io.ReadFull(c.out, body); err != nil {
		return nil, fmt.Errorf("git cat-file: %w", err)
	}
	return body[:size], nil
}

func (c *catFile) close() error {
	_ = c.in.Close()
	return c.cmd.Wait()
}
