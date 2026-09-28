//go:build ignore

// A pinned zstd implementation for the publisher, so the runner needs no zstd
// package:
//
//	zstd-image compress RAW OUTPUT   compresses a VM disk; never replaces OUTPUT
//	zstd-image measure FILE LIMIT    prints the decompressed digest and size as JSON
//
// measure decodes as the CLI does, with a 128 MiB window and an output bound.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"os"
	"strconv"

	"github.com/klauspost/compress/zstd"
)

func main() {
	if len(os.Args) != 4 || (os.Args[1] != "compress" && os.Args[1] != "measure") {
		log.Fatal("usage: zstd-image compress RAW OUTPUT | zstd-image measure FILE LIMIT")
	}
	input, err := os.Open(os.Args[2])
	if err != nil {
		log.Fatal(err)
	}
	defer input.Close()
	if os.Args[1] == "measure" {
		measure(input, os.Args[3])
		return
	}
	output, err := os.OpenFile(os.Args[3], os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		log.Fatal(err)
	}
	encoder, err := zstd.NewWriter(output, zstd.WithEncoderConcurrency(2), zstd.WithWindowSize(8<<20))
	if err != nil {
		log.Fatal(err)
	}
	if _, err = io.Copy(encoder, input); err != nil {
		log.Fatal(err)
	}
	if err = encoder.Close(); err != nil {
		log.Fatal(err)
	}
	if err = output.Sync(); err != nil {
		log.Fatal(err)
	}
	if err = output.Close(); err != nil {
		log.Fatal(err)
	}
}

func measure(input io.Reader, bound string) {
	limit, err := strconv.ParseInt(bound, 10, 64)
	if err != nil || limit < 1 {
		log.Fatal("invalid limit")
	}
	decoder, err := zstd.NewReader(input, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(128<<20), zstd.WithDecoderMaxWindow(128<<20))
	if err != nil {
		log.Fatal(err)
	}
	defer decoder.Close()
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(decoder, limit+1))
	if err != nil {
		log.Fatal(err)
	}
	if n > limit {
		log.Fatal("decompressed size exceeds the limit")
	}
	json.NewEncoder(os.Stdout).Encode(map[string]any{"digest": "sha256:" + hex.EncodeToString(hash.Sum(nil)), "size": n})
}
