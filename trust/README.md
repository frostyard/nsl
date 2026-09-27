# Image verification trust

`sigstore-root.json` is the Sigstore public-good trusted root fetched through
sigstore-go v1.3.0's authenticated TUF client on 2026-09-27. It is embedded in
released binaries; image downloads never replace it.

To prepare a reviewed trust update, run from the repository root:

```sh
go run scripts/refresh-sigstore-root.go
make ci
```

Review changed certificate authorities, transparency-log keys and validity
periods before committing and releasing. Verify a current Frostyard publication
bundle against the proposed root. Keep historical trust needed to validate
retained approved images. A failed verification is not permission to relax the
publisher, issuer, certificate-transparency or inclusion-proof requirements.

The application policy and rotation decision are in
[ADR-0015](../docs/adr/0015-image-verification-and-catalogue-policy.md) and the
[delivery contract](../docs/specs/image-delivery.md).
