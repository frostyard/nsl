package main

import (
	"errors"
	"testing"

	"golang.org/x/sys/unix"
)

func TestOpenat2Retry(t *testing.T) {
	t.Run("transient", func(t *testing.T) {
		calls := 0
		open := func(int, string, *unix.OpenHow) (int, error) {
			calls++
			if calls < openat2Attempts {
				return -1, unix.EAGAIN
			}
			return 42, nil
		}
		fd, err := openat2Retry(open, 1, "path", &unix.OpenHow{})
		if err != nil || fd != 42 || calls != openat2Attempts {
			t.Fatalf("fd=%d error=%v calls=%d", fd, err, calls)
		}
	})

	t.Run("exhausted", func(t *testing.T) {
		calls := 0
		open := func(int, string, *unix.OpenHow) (int, error) {
			calls++
			return -1, unix.EAGAIN
		}
		fd, err := openat2Retry(open, 1, "path", &unix.OpenHow{})
		if fd != -1 || !errors.Is(err, unix.EAGAIN) || calls != openat2Attempts {
			t.Fatalf("fd=%d error=%v calls=%d", fd, err, calls)
		}
	})
}
