package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"

	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/verify"
)

func TestSigstoreVerificationPolicy(t *testing.T) {
	signature, err := os.ReadFile("testdata/sigstore/provenance.json")
	if err != nil {
		t.Fatal(err)
	}
	trusted, err := root.NewTrustedRootFromJSON(sigstoreRoot)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := hex.DecodeString("46d4e2f74c4877316640000a6fdf8a8b59f1e0847667973e9859f774dd31b8f1e0937813b777fb66a2ac67d50540fe34640966eee9fc2ccca387082b4c85cd3c")
	if err != nil {
		t.Fatal(err)
	}
	publisher := "https://github.com/sigstore/sigstore-js/.github/workflows/release.yml@refs/heads/main"
	artifact := verify.WithArtifactDigest("sha512", digest)
	if err := verifyPublisherSignature(signature, trusted, imageIssuer, publisher, artifact); err != nil {
		t.Fatalf("valid public-good fixture: %v", err)
	}
	for _, tc := range []struct{ name, issuer, publisher string }{
		{"wrong publisher", imageIssuer, imagePublisher},
		{"wrong issuer", "https://attacker.invalid", publisher},
		{"branch substitution", imageIssuer, "https://github.com/sigstore/sigstore-js/.github/workflows/release.yml@refs/heads/other"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := verifyPublisherSignature(signature, trusted, tc.issuer, tc.publisher, artifact); err == nil {
				t.Fatal("accepted wrong identity")
			}
		})
	}
	if err := verifyPublisherSignature(signature, trusted, imageIssuer, publisher, verify.WithArtifact(bytes.NewReader([]byte("altered metadata")))); err == nil {
		t.Fatal("accepted different payload")
	}
	for _, name := range []string{"no log", "no inclusion proof", "altered signature", "altered checkpoint"} {
		t.Run(name, func(t *testing.T) {
			var b map[string]any
			if err := json.Unmarshal(signature, &b); err != nil {
				t.Fatal(err)
			}
			switch name {
			case "no log":
				b["verificationMaterial"].(map[string]any)["tlogEntries"] = []any{}
			case "no inclusion proof":
				delete(b["verificationMaterial"].(map[string]any)["tlogEntries"].([]any)[0].(map[string]any), "inclusionProof")
			case "altered signature":
				b["dsseEnvelope"].(map[string]any)["signatures"].([]any)[0].(map[string]any)["sig"] = "AAAA"
			case "altered checkpoint":
				b["verificationMaterial"].(map[string]any)["tlogEntries"].([]any)[0].(map[string]any)["inclusionProof"].(map[string]any)["checkpoint"].(map[string]any)["envelope"] = "invalid"
			}
			if err := verifyPublisherSignature(encodeJSON(b), trusted, imageIssuer, publisher, artifact); err == nil {
				t.Fatal("accepted altered bundle")
			}
		})
	}
	if err := verifyImageSignature([]byte("test"), signature); err == nil {
		t.Fatal("production policy accepted unrelated signature")
	}
}
