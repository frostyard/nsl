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

const testVMDescriptor = `{"schema":1,"role":"vm","build_id":"nsl-vm-trixie-x86-64-r1","distribution":"debian","release":"trixie","architecture":"x86-64","revision":1,"agent_protocol":1,"machine_protocol":1,"transport":"nsl-vsock-ssh","systemd":"257","kernel":"6.12","integration_sha256":"x","recipes_revision":"r","mkosi_revision":"m"}`
const testMachineDescriptor = `{"schema":1,"role":"machine","build_id":"nsl-machine-debian-trixie-x86-64-r1","distribution":"debian","release":"trixie","architecture":"x86-64","family":"debian","revision":1,"machine_protocol":1,"os_id":"debian","os_version":"13","systemd":"257","capabilities":{"gui":{},"nesting":{}},"integration_sha256":"x","recipes_revision":"r","mkosi_revision":"m"}`

func testCatalogue() imageCatalogue {
	now := time.Now().UTC().Truncate(time.Second)
	return imageCatalogue{Schema: 1, Sequence: 10, Created: now, Expires: now.Add(24 * time.Hour), Revoked: []string{}, Images: []catalogueEntry{
		{Kind: "vm", Architecture: "x86-64", AgentProtocol: 1, BuildID: "nsl-vm-trixie-x86-64-r1", Manifest: hashBytes([]byte("vm manifest"))},
		{Kind: "machine", Selectors: []string{"debian:trixie", "debian:13"}, Architecture: "x86-64", MachineProtocol: 1,
			BuildID: "nsl-machine-debian-trixie-x86-64-r1", Manifest: hashBytes([]byte("machine manifest"))},
	}}
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
		{"duplicate selector", func(c *imageCatalogue) { c.Images[1].Selectors = append(c.Images[1].Selectors, "debian:13") }},
		{"selector in two entries", func(c *imageCatalogue) {
			other := c.Images[1]
			other.Manifest, other.Selectors = hashBytes([]byte("other")), []string{"debian:13"}
			c.Images = append(c.Images, other)
		}},
		{"revoked entry", func(c *imageCatalogue) { c.Revoked = []string{c.Images[1].Manifest} }},
		{"invalid digest", func(c *imageCatalogue) { c.Images[1].Manifest = "sha256:../../image" }},
		{"invalid schema", func(c *imageCatalogue) { c.Schema = 2 }},
		{"unknown kind", func(c *imageCatalogue) { c.Images[0].Kind = "disk" }},
		{"vm with selectors", func(c *imageCatalogue) { c.Images[0].Selectors = []string{"vm:1"} }},
		{"vm without protocol", func(c *imageCatalogue) { c.Images[0].AgentProtocol = 0 }},
		{"vm with machine protocol", func(c *imageCatalogue) { c.Images[0].MachineProtocol = 1 }},
		{"machine without selectors", func(c *imageCatalogue) { c.Images[1].Selectors = nil }},
		{"machine without protocol", func(c *imageCatalogue) { c.Images[1].MachineProtocol = 0 }},
		{"machine with agent protocol", func(c *imageCatalogue) { c.Images[1].AgentProtocol = 1 }},
		{"two VM images", func(c *imageCatalogue) {
			other := c.Images[0]
			other.Manifest = hashBytes([]byte("other vm"))
			c.Images = append(c.Images, other)
		}},
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
	// VM images for another agent protocol may sit beside this one.
	other := base
	other.Images = append(append([]catalogueEntry{}, base.Images...), catalogueEntry{Kind: "vm", Architecture: "x86-64", AgentProtocol: 2,
		BuildID: "nsl-vm-trixie-x86-64-r9", Manifest: hashBytes([]byte("next vm"))})
	if err := other.validate(time.Now()); err != nil {
		t.Fatal(err)
	}
	if entry, err := selectVM(other); err != nil || entry.AgentProtocol != 1 {
		t.Fatal(entry, err)
	}
}

func TestSelection(t *testing.T) {
	base := testCatalogue()
	for _, selector := range []string{"debian:13", "debian:trixie@" + base.Images[1].Manifest} {
		if entry, err := selectMachine(base, selector); err != nil || entry.Kind != "machine" {
			t.Fatal(entry, err)
		}
	}
	for _, selector := range []string{"../debian:13", "debian:13@" + hashBytes([]byte("wrong")), "other:13", "debian:13@" + base.Images[0].Manifest} {
		if _, err := selectMachine(base, selector); err == nil {
			t.Fatalf("accepted %s", selector)
		}
	}
	newer := testCatalogue()
	newer.Images[1].MachineProtocol = 2
	if _, err := selectMachine(newer, "debian:13"); !errors.Is(err, errIncompatibleImage) {
		t.Fatal(err)
	}
	newer.Images[0].AgentProtocol = 2
	if _, err := selectVM(newer); err == nil {
		t.Fatal("selected a VM image for another agent protocol")
	}
}

func TestArtifactValidation(t *testing.T) {
	ref := refBytes([]byte("x"))
	vm := imageArtifact{Schema: 1, Kind: "vm", Image: json.RawMessage(testVMDescriptor), Raw: &ref, Compressed: ref, Packages: ref, Provenance: ref, Acceptance: ref}
	machine := imageArtifact{Schema: 1, Kind: "machine", Image: json.RawMessage(testMachineDescriptor), Rootfs: &ref, Compressed: ref, Packages: ref, Provenance: ref, Acceptance: ref}
	if build, version, err := vm.validate(vmKind); err != nil || build != "nsl-vm-trixie-x86-64-r1" || version != 1 {
		t.Fatal(build, version, err)
	}
	if build, version, err := machine.validate(machineKind); err != nil || build != "nsl-machine-debian-trixie-x86-64-r1" || version != 1 {
		t.Fatal(build, version, err)
	}
	for name, tc := range map[string]struct {
		artifact imageArtifact
		kind     imageKind
	}{
		"vm as machine":    {vm, machineKind},
		"machine as vm":    {machine, vmKind},
		"vm with rootfs":   {func() imageArtifact { a := vm; a.Rootfs = &ref; return a }(), vmKind},
		"machine with raw": {func() imageArtifact { a := machine; a.Raw = &ref; return a }(), machineKind},
		"agent protocol": {func() imageArtifact {
			a := vm
			a.Image = replace(testVMDescriptor, `"agent_protocol":1`, `"agent_protocol":2`)
			return a
		}(), vmKind},
		"machine protocol": {func() imageArtifact {
			a := machine
			a.Image = replace(testMachineDescriptor, `"machine_protocol":1`, `"machine_protocol":2`)
			return a
		}(), machineKind},
		"architecture": {func() imageArtifact {
			a := machine
			a.Image = replace(testMachineDescriptor, `"x86-64"`, `"arm64"`)
			return a
		}(), machineKind},
		"role": {func() imageArtifact {
			a := machine
			a.Image = replace(testMachineDescriptor, `"role":"machine"`, `"role":"vm"`)
			return a
		}(), machineKind},
		"unknown field": {func() imageArtifact {
			a := machine
			a.Image = replace(testMachineDescriptor, `{`, `{"extra":1,`)
			return a
		}(), machineKind},
		"oversized rootfs":     {func() imageArtifact { a := machine; big := blobRef{ref.Digest, 16<<30 + 1}; a.Rootfs = &big; return a }(), machineKind},
		"oversized compressed": {func() imageArtifact { a := machine; a.Compressed.Size = 4<<30 + 1; return a }(), machineKind},
	} {
		if _, _, err := tc.artifact.validate(tc.kind); err == nil {
			t.Fatal("accepted", name)
		}
	}
}

func replace(s, old, new string) json.RawMessage {
	return json.RawMessage(strings.Replace(s, old, new, 1))
}

func TestMetadataAmbiguityAndBounds(t *testing.T) {
	for _, value := range []string{`{"schema":1,"schema":2}`, `{"nested":{"x":1,"x":2}}`, `{} {}`, strings.Repeat("[", 34) + "0" + strings.Repeat("]", 34)} {
		var v json.RawMessage
		if decodeMetadata([]byte(value), &v, metadataLimit) == nil {
			t.Fatalf("accepted ambiguous metadata: %s", value)
		}
	}
	var d machineDescriptor
	if decodeMetadata([]byte(`{"unexpected":1}`), &d, metadataLimit) == nil {
		t.Fatal("accepted unknown descriptor field")
	}
	if decodeMetadata([]byte(`{}`), &d, 1) == nil {
		t.Fatal("accepted oversized metadata")
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
	m := ociManifest{SchemaVersion: 2, MediaType: ociManifestType, ArtifactType: machineKind.artifactType,
		Config: ociLayer{blobRef: refBytes([]byte("{}")), MediaType: "application/vnd.oci.empty.v1+json", Data: []byte("{}")}}
	b := encodeJSON(m)
	if _, err := parseManifest(b, hashBytes(b), machineKind.artifactType); err != nil {
		t.Fatal(err)
	}
	if _, err := parseManifest(b, hashBytes(b), vmKind.artifactType); err == nil {
		t.Fatal("accepted a machine artifact as a VM image")
	}
	m.Config.Data = []byte("unexpected config")
	b = encodeJSON(m)
	if _, err := parseManifest(b, hashBytes(b), machineKind.artifactType); err == nil {
		t.Fatal("accepted nonempty config")
	}
}
