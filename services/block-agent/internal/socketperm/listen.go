package socketperm

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
)

// Listen creates a Unix socket inside a group-isolated directory. On Linux the
// service account must own the directory and be a member of socketGroup.
func Listen(path, socketGroup string) (net.Listener, error) {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return nil, err
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("socket directory %s must be a real directory", directory)
	}
	if err := setGroup(directory, socketGroup); err != nil {
		return nil, fmt.Errorf("set socket directory group: %w", err)
	}
	if err := os.Chmod(directory, 0o750); err != nil {
		return nil, err
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("refusing to replace non-socket path %s", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := setGroup(path, socketGroup); err != nil {
		_ = listener.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("set socket group: %w", err)
	}
	if err := os.Chmod(path, 0o660); err != nil {
		_ = listener.Close()
		_ = os.Remove(path)
		return nil, err
	}
	return listener, nil
}

func setGroup(path, name string) error {
	if name == "" {
		return errors.New("socket group is required")
	}
	// Windows supports AF_UNIX for local tests but has no POSIX group ownership.
	// Configuration is still required and Linux deployment tests verify it.
	if runtime.GOOS == "windows" {
		return nil
	}
	group, err := user.LookupGroup(name)
	if err != nil {
		return fmt.Errorf("lookup group %q: %w", name, err)
	}
	gid, err := strconv.Atoi(group.Gid)
	if err != nil {
		return fmt.Errorf("parse group %q id %q: %w", name, group.Gid, err)
	}
	return os.Chown(path, -1, gid)
}
