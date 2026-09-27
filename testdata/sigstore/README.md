# Sigstore verification fixture

`provenance.json` is the upstream `sigstore.js@2.0.0-provenance.sigstore.json`
fixture from [sigstore-go v1.3.0](https://github.com/sigstore/sigstore-go/tree/v1.3.0/pkg/testing/data/bundles).
It is distributed under the accompanying Apache-2.0 license.

Tests verify its real certificate, transparency evidence, signing identity and
artifact digest using the embedded public-good root. They then reject changed
identities, payload, signature and log evidence. The production verifier always
requires Frostyard's publishing identity; no test identity is configurable in the CLI.
