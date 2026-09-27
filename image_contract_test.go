package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func validImageDescriptor() imageDescriptor {
	return imageDescriptor{Schema: 1, BuildID: "test-debian-v1", Distribution: "debian", Release: "trixie", Architecture: "x86-64", RootFilesystem: "btrfs", ProtocolMin: 1, ProtocolMax: 1, Transport: "nsl-vsock-ssh"}
}
func testCatalogue() imageCatalogue {
	now := time.Now().UTC().Truncate(time.Second)
	return imageCatalogue{Schema: 1, Sequence: 10, Created: now, Expires: now.Add(24 * time.Hour), Images: []catalogueEntry{{Selectors: []string{"debian:trixie", "debian:13"}, Architecture: "x86-64", BuildID: "test-debian-v1", Manifest: hashBytes([]byte("manifest"))}}}
}
func TestCataloguePolicy(t *testing.T) {
	base := testCatalogue()
	if err := base.validate(time.Now()); err != nil {
		t.Fatal(err)
	}
	first := base
	first.Sequence = catalogueMinimum
	if err := first.validate(time.Now()); err != nil {
		t.Fatalf("first supported catalogue: %v", err)
	}
	for _, tc := range []struct {
		name   string
		change func(*imageCatalogue)
	}{
		{"expired", func(c *imageCatalogue) { c.Expires = time.Now().Add(-time.Second) }},
		{"future", func(c *imageCatalogue) { c.Created = time.Now().Add(6 * time.Minute) }},
		{"long validity", func(c *imageCatalogue) { c.Expires = c.Created.Add(31 * 24 * time.Hour) }},
		{"old sequence", func(c *imageCatalogue) { c.Sequence = catalogueMinimum - 1 }},
		{"duplicate selector", func(c *imageCatalogue) { c.Images[0].Selectors = append(c.Images[0].Selectors, "debian:13") }},
		{"revoked entry", func(c *imageCatalogue) { c.Revoked = []string{c.Images[0].Manifest} }},
		{"invalid digest", func(c *imageCatalogue) { c.Images[0].Manifest = "sha256:../../image" }},
		{"invalid schema", func(c *imageCatalogue) { c.Schema = 2 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var c imageCatalogue
			if err := json.Unmarshal(encodeJSON(base), &c); err != nil {
				t.Fatal(err)
			}
			tc.change(&c)
			if c.validate(time.Now()) == nil {
				t.Fatal("accepted invalid catalogue")
			}
		})
	}
	for _, selector := range []string{"debian:13", "debian:trixie@" + base.Images[0].Manifest} {
		if _, err := selectImage(base, selector); err != nil {
			t.Fatal(err)
		}
	}
	for _, selector := range []string{"../debian:13", "debian:13@" + hashBytes([]byte("wrong")), "other:13"} {
		if _, err := selectImage(base, selector); err == nil {
			t.Fatalf("accepted %s", selector)
		}
	}
}
func TestMetadataAmbiguityAndBounds(t *testing.T) {
	for _, value := range []string{`{"schema":1,"schema":2}`, `{"nested":{"x":1,"x":2}}`, `{} {}`, strings.Repeat("[", 34) + "0" + strings.Repeat("]", 34)} {
		var v json.RawMessage
		if decodeMetadata([]byte(value), &v, metadataLimit) == nil {
			t.Fatalf("accepted ambiguous metadata: %s", value)
		}
	}
	var d imageDescriptor
	if decodeMetadata([]byte(`{"unexpected":1}`), &d, metadataLimit) == nil {
		t.Fatal("accepted unknown descriptor field")
	}
	if decodeMetadata([]byte(`{}`), &d, 1) == nil {
		t.Fatal("accepted oversized metadata")
	}
}
func TestIncompatibleGuestFailsImmediately(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*imageDescriptor)
	}{
		{"architecture", func(d *imageDescriptor) { d.Architecture = "arm64" }},
		{"protocol", func(d *imageDescriptor) { d.ProtocolMin = 2; d.ProtocolMax = 2 }},
		{"schema", func(d *imageDescriptor) { d.Schema = 2 }},
		{"transport", func(d *imageDescriptor) { d.Transport = "unknown" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, f, e := fixture(t)
			d := validImageDescriptor()
			tc.change(&d)
			f.guestDescriptor = &d
			if err := a.start(e); !errors.Is(err, errIncompatibleImage) {
				t.Fatalf("got %v", err)
			}
		})
	}
}
func TestMetadataCaptureBoundAppliesToCopy(t *testing.T) {
	b := &boundedBuffer{limit: 32}
	// Hide Reader.WriteTo so io.Copy would use an accidentally promoted ReadFrom.
	reader := struct{ io.Reader }{bytes.NewReader(bytes.Repeat([]byte("x"), 100))}
	if _, err := io.Copy(b, reader); !errors.Is(err, io.ErrShortBuffer) {
		t.Fatalf("got %v", err)
	}
	if b.buffer.Len() != 32 {
		t.Fatalf("stored %d bytes", b.buffer.Len())
	}
}

func TestOCIArtifactInlineEmptyConfig(t *testing.T) {
	m := ociManifest{SchemaVersion: 2, MediaType: ociManifestType, ArtifactType: diskArtifactType,
		Config: ociLayer{blobRef: refBytes([]byte("{}")), MediaType: "application/vnd.oci.empty.v1+json", Data: []byte("{}")}}
	b := encodeJSON(m)
	if _, err := parseManifest(b, hashBytes(b), diskArtifactType); err != nil {
		t.Fatal(err)
	}
	m.Config.Data = []byte("unexpected config")
	b = encodeJSON(m)
	if _, err := parseManifest(b, hashBytes(b), diskArtifactType); err == nil {
		t.Fatal("accepted nonempty config")
	}
}
