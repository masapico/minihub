//go:build windows

package shutdown

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/sys/windows"
)

// Event names are tied to the absolute data directory, not the executable name.
// The global namespace also works when the start and backup tasks run in
// different Windows sessions under the same account.
func eventName(dataDir string) (*uint16, error) {
	path, err := filepath.Abs(dataDir)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(path))))
	return windows.UTF16PtrFromString(fmt.Sprintf("Global\\minihub-stop-%x", sum))
}

func Listen(dataDir string) (<-chan struct{}, func() error, error) {
	name, err := eventName(dataDir)
	if err != nil {
		return nil, nil, err
	}
	handle, err := windows.CreateEvent(nil, 1, 0, name)
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		windows.CloseHandle(handle)
		return nil, nil, errors.New("shutdown event already exists for data directory")
	}
	if err != nil {
		return nil, nil, err
	}
	requested := make(chan struct{})
	done := make(chan struct{})
	var once sync.Once
	var worker sync.WaitGroup
	worker.Add(1)
	go func() {
		defer worker.Done()
		for {
			result, waitErr := windows.WaitForSingleObject(handle, 250)
			if waitErr != nil {
				return
			}
			if result == windows.WAIT_OBJECT_0 {
				close(requested)
				return
			}
			select {
			case <-done:
				return
			default:
			}
		}
	}()
	closeEvent := func() error {
		once.Do(func() { close(done) })
		worker.Wait()
		return windows.CloseHandle(handle)
	}
	return requested, closeEvent, nil
}

func Request(dataDir string) (bool, error) {
	name, err := eventName(dataDir)
	if err != nil {
		return false, err
	}
	handle, err := windows.OpenEvent(windows.EVENT_MODIFY_STATE, false, name)
	if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer windows.CloseHandle(handle)
	return true, windows.SetEvent(handle)
}
