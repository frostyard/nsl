//go:build ignore

// Run explicitly with: go run scripts/refresh-sigstore-root.go
// This refreshes the reviewed release input, never runtime client trust.
package main

import (
	"log"
	"os"

	"github.com/sigstore/sigstore-go/pkg/root"
)

func main() {
	trusted, err := root.FetchTrustedRoot()
	if err != nil {
		log.Fatal(err)
	}
	data, err := trusted.MarshalJSON()
	if err != nil {
		log.Fatal(err)
	}
	if err = os.WriteFile("trust/sigstore-root.json", append(data, '\n'), 0644); err != nil {
		log.Fatal(err)
	}
}
