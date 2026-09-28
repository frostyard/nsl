package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/frostyard/nsl/internal/protocol"
	"github.com/klauspost/compress/zstd"
)

// export writes a stopped machine's root filesystem to stdout as a zstd tar
// that keeps numeric owners, modes, xattrs and ACLs. The stream passes the
// same validation as an import, so a published archive can be imported.
func (a *agent) export(req *protocol.Request) error {
	release, err := a.lockMachine(req.Machine, lockWait)
	if err != nil {
		return err
	}
	defer release()
	if _, err = a.record(req); err != nil {
		return err
	}
	if err = a.requireStopped(req.Machine); err != nil {
		return err
	}
	dir := a.path(machinesDir + "/" + req.Machine)
	if st, err := os.Lstat(dir); err != nil || !st.IsDir() {
		return &protocol.Error{Code: protocol.CodeFailed, Message: "the root filesystem of " + req.Machine + " is missing"}
	}
	encoder, err := zstd.NewWriter(a.stdout, zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		return err
	}
	reader, writer := io.Pipe()
	var stderr bytes.Buffer
	archived := make(chan error, 1)
	go func() {
		err := a.r.run(context.Background(), nil, writer, &stderr, "tar", "--create", "--file=-", "--directory", dir,
			"--format=posix", "--numeric-owner", "--xattrs", "--xattrs-include=*", "--acls", ".")
		// Report before closing, so the validator's read error means tar ended first.
		archived <- err
		writer.CloseWithError(err)
	}()
	invalid := validateTar(io.TeeReader(reader, encoder), protocol.ArchiveLimit, true)
	var failed error
	select {
	case failed = <-archived:
	default:
		// The validator refused the stream while tar was still writing it.
		reader.CloseWithError(io.ErrClosedPipe)
		<-archived
	}
	if failed != nil {
		return fmt.Errorf("archiving %s: %v: %s", req.Machine, failed, strings.TrimSpace(stderr.String()))
	}
	if invalid != nil {
		return invalid
	}
	return encoder.Close()
}

// importMachine builds a machine from an archived root filesystem on stdin.
func (a *agent) importMachine(req *protocol.Request, binding *protocol.Binding) error {
	receive := func(destination string) error {
		return copyVerified(a.stdin, req.Rootfs.Digest, req.Rootfs.Size, destination)
	}
	return a.prepare(req, binding, receive, true, req.Rootfs.BuildID)
}
