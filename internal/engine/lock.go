package engine

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

const (
	lockDirectoryPermissions os.FileMode = 0o700
	lockFilePermissions      os.FileMode = 0o600
)

// FileRunLocker owns the descriptor that keeps one per-config flock held.
type FileRunLocker struct {
	lockFile *os.File
}

// NewFileRunLocker creates the concrete locking boundary used by the CLI.
func NewFileRunLocker() *FileRunLocker {
	return &FileRunLocker{}
}

// Acquire obtains a fail-closed, nonblocking lock for an absolute config identity.
func (l *FileRunLocker) Acquire(configPath string) error {
	if l.lockFile != nil {
		return errors.New("acquiring run lock: locker already holds a lock")
	}

	lockPath, err := lockFilePath(configPath)
	if err != nil {
		return fmt.Errorf("resolving lock path: %w", err)
	}
	lockFile, err := os.OpenFile(
		lockPath,
		os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW,
		lockFilePermissions,
	)
	if err != nil {
		return fmt.Errorf("opening lock file %s: %w", lockPath, err)
	}
	if err := validateLockFile(lockFile, lockPath); err != nil {
		return closeUnacquiredLockFile(lockFile, err)
	}
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return closeUnacquiredLockFile(lockFile, classifyRunLockError(lockPath, err))
	}
	l.lockFile = lockFile
	return nil
}

func classifyRunLockError(lockPath string, cause error) error {
	if errors.Is(cause, syscall.EWOULDBLOCK) {
		return fmt.Errorf("another instance is already running (lock: %s): %w", lockPath, cause)
	}
	return fmt.Errorf("locking file %s: %w", lockPath, cause)
}

func closeUnacquiredLockFile(lockFile *os.File, acquisitionError error) error {
	if closeError := lockFile.Close(); closeError != nil {
		return errors.Join(
			acquisitionError,
			fmt.Errorf("closing unacquired lock file %s: %w", lockFile.Name(), closeError),
		)
	}
	return acquisitionError
}

func lockFilePath(configPath string) (string, error) {
	directory, err := lockDirectory()
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256([]byte(configPath))
	return filepath.Join(directory, fmt.Sprintf("shuttle-%x.lock", hash[:4])), nil
}

func lockDirectory() (string, error) {
	directory := os.Getenv("XDG_RUNTIME_DIR")
	if directory != "" {
		if !filepath.IsAbs(directory) {
			return "", fmt.Errorf("XDG_RUNTIME_DIR %q is not absolute", directory)
		}
	} else {
		temporaryDirectory := os.TempDir()
		if !filepath.IsAbs(temporaryDirectory) {
			return "", fmt.Errorf("temporary directory root %q is not absolute", temporaryDirectory)
		}
		directory = filepath.Join(temporaryDirectory, fmt.Sprintf("shuttle-%d", os.Getuid()))
	}
	if err := ensureSecureLockDirectory(directory); err != nil {
		return "", err
	}
	return directory, nil
}

func ensureSecureLockDirectory(directory string) error {
	info, err := os.Lstat(directory)
	if errors.Is(err, fs.ErrNotExist) {
		if err := os.Mkdir(directory, lockDirectoryPermissions); err != nil {
			return fmt.Errorf("creating lock directory %s: %w", directory, err)
		}
		info, err = os.Lstat(directory)
	}
	if err != nil {
		return fmt.Errorf("checking lock directory %s: %w", directory, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("lock directory %s is a symlink", directory)
	}
	if !info.IsDir() {
		return fmt.Errorf("lock directory %s is not a directory", directory)
	}
	if info.Mode().Perm() != lockDirectoryPermissions {
		return fmt.Errorf(
			"lock directory %s has permissions %o, want %o",
			directory,
			info.Mode().Perm(),
			lockDirectoryPermissions,
		)
	}
	return validateCurrentUserOwnership(info, directory, "lock directory")
}

func validateLockFile(lockFile *os.File, lockPath string) error {
	pathInfo, err := os.Lstat(lockPath)
	if err != nil {
		return fmt.Errorf("checking lock file %s: %w", lockPath, err)
	}
	if pathInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("lock file %s is a symlink", lockPath)
	}
	if !pathInfo.Mode().IsRegular() {
		return fmt.Errorf("lock file %s is not a regular file", lockPath)
	}
	if pathInfo.Mode().Perm() != lockFilePermissions {
		return fmt.Errorf(
			"lock file %s has permissions %o, want %o",
			lockPath,
			pathInfo.Mode().Perm(),
			lockFilePermissions,
		)
	}
	if err := validateCurrentUserOwnership(pathInfo, lockPath, "lock file"); err != nil {
		return err
	}
	descriptorInfo, err := lockFile.Stat()
	if err != nil {
		return fmt.Errorf("checking opened lock file %s: %w", lockPath, err)
	}
	if !os.SameFile(pathInfo, descriptorInfo) {
		return fmt.Errorf("lock file %s changed identity during open", lockPath)
	}
	return nil
}

func validateCurrentUserOwnership(info os.FileInfo, path, kind string) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("%s %s has unsupported ownership metadata", kind, path)
	}
	if int(stat.Uid) != os.Getuid() {
		return fmt.Errorf("%s %s is not owned by current user", kind, path)
	}
	return nil
}
