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

	configIdentity, err := canonicalConfigIdentity(configPath)
	if err != nil {
		return fmt.Errorf("resolving config identity: %w", err)
	}
	lockPath, err := lockFilePath(configIdentity)
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

// Release unlocks and closes the descriptor held by a successful Acquire.
func (l *FileRunLocker) Release() error {
	if l.lockFile == nil {
		return nil
	}
	lockFile := l.lockFile
	l.lockFile = nil

	var releaseErrors []error
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN); err != nil {
		releaseErrors = append(
			releaseErrors,
			fmt.Errorf("unlocking file %s: %w", lockFile.Name(), err),
		)
	}
	if err := lockFile.Close(); err != nil {
		releaseErrors = append(
			releaseErrors,
			fmt.Errorf("closing lock file %s: %w", lockFile.Name(), err),
		)
	}
	return errors.Join(releaseErrors...)
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
	if !filepath.IsAbs(configPath) {
		return "", fmt.Errorf("config path %q is not absolute", configPath)
	}
	directory, err := lockDirectory()
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256([]byte(filepath.Clean(configPath)))
	return filepath.Join(directory, fmt.Sprintf("shuttle-%x.lock", hash[:])), nil
}

func canonicalConfigIdentity(configPath string) (string, error) {
	if !filepath.IsAbs(configPath) {
		return "", fmt.Errorf("config path %q is not absolute", configPath)
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(configPath))
	if err != nil {
		return "", fmt.Errorf("resolving config path %s: %w", configPath, err)
	}
	if !filepath.IsAbs(resolved) {
		return "", fmt.Errorf("resolved config path %q is not absolute", resolved)
	}
	return filepath.Clean(resolved), nil
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
	canonicalDirectory, err := canonicalLockDirectoryPath(directory)
	if err != nil {
		return "", err
	}
	directory = canonicalDirectory
	if err := ensureSecureLockDirectory(directory); err != nil {
		return "", err
	}
	return directory, nil
}

func canonicalLockDirectoryPath(directory string) (string, error) {
	info, err := os.Lstat(directory)
	switch {
	case err == nil:
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("lock directory %s is a symlink", directory)
		}
		resolved, resolveErr := filepath.EvalSymlinks(directory)
		if resolveErr != nil {
			return "", fmt.Errorf("resolving lock directory %s: %w", directory, resolveErr)
		}
		return resolved, nil
	case errors.Is(err, fs.ErrNotExist):
		parent, resolveErr := filepath.EvalSymlinks(filepath.Dir(directory))
		if resolveErr != nil {
			return "", fmt.Errorf(
				"resolving lock directory parent %s: %w",
				filepath.Dir(directory),
				resolveErr,
			)
		}
		return filepath.Join(parent, filepath.Base(directory)), nil
	default:
		return "", fmt.Errorf("checking lock directory %s: %w", directory, err)
	}
}

func ensureSecureLockDirectory(directory string) error {
	canonicalDirectory, err := canonicalLockDirectoryPath(directory)
	if err != nil {
		return err
	}
	directory = canonicalDirectory

	_, err = os.Lstat(directory)
	if errors.Is(err, fs.ErrNotExist) {
		if err := validateSecureLockDirectoryAncestors(filepath.Dir(directory)); err != nil {
			return err
		}
		if mkdirErr := os.Mkdir(directory, lockDirectoryPermissions); mkdirErr != nil &&
			!errors.Is(mkdirErr, fs.ErrExist) {
			return fmt.Errorf("creating lock directory %s: %w", directory, mkdirErr)
		}
		_, err = os.Lstat(directory)
	}
	if err != nil {
		return fmt.Errorf("checking lock directory %s: %w", directory, err)
	}
	return validateSecureLockDirectory(directory)
}

func validateSecureLockDirectoryAncestors(directory string) error {
	cleanDirectory := filepath.Clean(directory)
	if !filepath.IsAbs(cleanDirectory) {
		return fmt.Errorf("lock directory %q is not absolute", directory)
	}

	var chain []string
	for current := cleanDirectory; ; current = filepath.Dir(current) {
		chain = append(chain, current)
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
	}

	for index := len(chain) - 1; index >= 0; index-- {
		path := chain[index]
		info, err := os.Lstat(path)
		if err != nil {
			return fmt.Errorf("checking lock directory path %s: %w", path, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("lock directory path %s is a symlink", path)
		}
		if !info.IsDir() {
			return fmt.Errorf("lock directory path %s is not a directory", path)
		}
		if err := validateTrustedDirectoryOwner(info, path); err != nil {
			return err
		}
		if info.Mode().Perm()&0o022 != 0 && info.Mode()&os.ModeSticky == 0 {
			return fmt.Errorf(
				"unsafe writable lock directory ancestor %s has permissions %o without sticky bit",
				path,
				info.Mode().Perm(),
			)
		}
	}
	return nil
}

func validateSecureLockDirectory(directory string) error {
	if err := validateSecureLockDirectoryAncestors(filepath.Dir(directory)); err != nil {
		return err
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return fmt.Errorf("checking lock directory %s: %w", directory, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("lock directory %s is a symlink", directory)
	}
	if !info.IsDir() {
		return fmt.Errorf("lock directory %s is not a directory", directory)
	}
	if err := validateTrustedDirectoryOwner(info, directory); err != nil {
		return err
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

func validateTrustedDirectoryOwner(info os.FileInfo, path string) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("lock directory path %s has unsupported ownership metadata", path)
	}
	uid := int(stat.Uid)
	if uid != 0 && uid != os.Getuid() {
		return fmt.Errorf("unsafe lock directory ancestor %s is not owned by root or current user", path)
	}
	return nil
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
