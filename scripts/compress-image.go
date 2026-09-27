//go:build ignore

// A pinned zstd implementation for the publisher; requires no host zstd package.
package main

import (
	"io"
	"log"
	"os"

	"github.com/klauspost/compress/zstd"
)

func main() {
	if len(os.Args) != 3 {
		log.Fatal("usage: compress-image RAW OUTPUT")
	}
	input, err := os.Open(os.Args[1])
	if err != nil {
		log.Fatal(err)
	}
	defer input.Close()
	output, err := os.OpenFile(os.Args[2], os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
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
