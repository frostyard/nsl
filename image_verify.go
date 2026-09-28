package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"

	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/verify"
)

//go:embed trust/sigstore-root.json
var sigstoreRoot []byte

const imagePublisher = "https://github.com/frostyard/nsl/.github/workflows/images.yml@refs/heads/main"
const imageIssuer = "https://token.actions.githubusercontent.com"

func verifyImageSignature(payload, signature []byte) error {
	trusted, err := root.NewTrustedRootFromJSON(sigstoreRoot)
	if err != nil {
		return err
	}
	if len(payload) > metadataLimit {
		return fmt.Errorf("signed metadata exceeds limit")
	}
	return verifyPublisherSignature(signature, trusted, imageIssuer, imagePublisher, verify.WithArtifact(bytes.NewReader(payload)))
}

// Parameters are internal test seams; the CLI always uses the embedded root and
// fixed publication identity above.
func verifyPublisherSignature(signature []byte, trusted root.TrustedMaterial, issuer, publisher string, artifact verify.ArtifactPolicyOption) error {
	if len(signature) > metadataLimit {
		return fmt.Errorf("signed metadata exceeds limit")
	}
	var b bundle.Bundle
	var raw json.RawMessage
	if err := decodeMetadata(signature, &raw, metadataLimit); err != nil {
		return err
	}
	if err := b.UnmarshalJSON(signature); err != nil {
		return fmt.Errorf("invalid Sigstore bundle: %w", err)
	}
	if !b.HasInclusionProof() {
		return fmt.Errorf("bundle lacks a Sigstore inclusion proof")
	}
	verifier, err := verify.NewVerifier(trusted, verify.WithSignedCertificateTimestamps(1), verify.WithTransparencyLog(1), verify.WithObserverTimestamps(1))
	if err != nil {
		return err
	}
	identity, err := verify.NewShortCertificateIdentity(issuer, "", publisher, "")
	if err != nil {
		return err
	}
	_, err = verifier.Verify(&b, verify.NewPolicy(artifact, verify.WithCertificateIdentity(identity)))
	if err != nil {
		return fmt.Errorf("image publisher verification failed: %w", err)
	}
	return nil
}
