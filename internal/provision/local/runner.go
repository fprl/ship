package local

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/fprl/ship/internal/provision/host"
)

type Runner struct{}

func (Runner) ReadFile(_ context.Context, path string) (host.FileState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return host.FileState{}, host.ErrNotExist
		}
		return host.FileState{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return host.FileState{}, err
	}
	owner, group := fileOwnerGroup(info)
	return host.FileState{
		Content: data,
		Owner:   owner,
		Group:   group,
		Mode:    info.Mode().Perm(),
	}, nil
}

func (Runner) WriteFile(_ context.Context, file host.File) error {
	if err := os.MkdirAll(filepath.Dir(file.Path), 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(file.Path), "."+filepath.Base(file.Path)+".")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		if _, err := os.Stat(tmpName); err == nil {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(file.Content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, file.Mode.Perm()); err != nil {
		return err
	}
	if err := chown(tmpName, file.Owner, file.Group); err != nil {
		return err
	}
	return os.Rename(tmpName, file.Path)
}

func (r Runner) Validate(ctx context.Context, validation host.Validation) error {
	switch validation.Kind {
	case host.ValidationSudoers:
		tmp, err := os.CreateTemp("", "ship-sudoers-")
		if err != nil {
			return err
		}
		tmpName := tmp.Name()
		defer os.Remove(tmpName)
		if _, err := tmp.Write(validation.Content); err != nil {
			_ = tmp.Close()
			return err
		}
		if err := tmp.Close(); err != nil {
			return err
		}
		result, err := r.Run(ctx, host.Command{Program: "visudo", Args: []string{"-cf", tmpName}})
		if err != nil {
			return err
		}
		if result.ExitCode != 0 {
			return fmt.Errorf("sudoers validation failed: %s", bytes.TrimSpace(result.Stderr))
		}
		return nil
	default:
		return fmt.Errorf("unsupported validation kind: %s", validation.Kind)
	}
}

func (Runner) Run(ctx context.Context, command host.Command) (host.CommandResult, error) {
	cmd := exec.CommandContext(ctx, command.Program, command.Args...)
	cmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	result := host.CommandResult{
		Stdout: stdout.Bytes(),
		Stderr: stderr.Bytes(),
	}
	if err == nil {
		return result, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
		return result, nil
	}
	return result, err
}

func fileOwnerGroup(info os.FileInfo) (string, string) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", ""
	}
	owner := strconv.FormatUint(uint64(stat.Uid), 10)
	group := strconv.FormatUint(uint64(stat.Gid), 10)
	if u, err := user.LookupId(owner); err == nil {
		owner = u.Username
	}
	if g, err := user.LookupGroupId(group); err == nil {
		group = g.Name
	}
	return owner, group
}

func chown(path string, ownerName string, groupName string) error {
	uid := -1
	gid := -1
	if ownerName != "" {
		u, err := user.Lookup(ownerName)
		if err != nil {
			return err
		}
		parsed, err := strconv.Atoi(u.Uid)
		if err != nil {
			return err
		}
		uid = parsed
	}
	if groupName != "" {
		g, err := user.LookupGroup(groupName)
		if err != nil {
			return err
		}
		parsed, err := strconv.Atoi(g.Gid)
		if err != nil {
			return err
		}
		gid = parsed
	}
	if uid == -1 && gid == -1 {
		return nil
	}
	return os.Chown(path, uid, gid)
}

var _ host.Runner = Runner{}
