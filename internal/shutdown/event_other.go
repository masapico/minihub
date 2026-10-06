//go:build !windows

package shutdown

import "errors"

var ErrWindowsOnly = errors.New("the stop command is available only on Windows")

func Listen(string) (<-chan struct{}, func() error, error) {
	return nil, nil, ErrWindowsOnly
}

func Request(string) (bool, error) {
	return false, ErrWindowsOnly
}
