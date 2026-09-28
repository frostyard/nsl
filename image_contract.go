package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/frostyard/nsl/internal/protocol"
)

// Signed delivery (docs/specs/image-delivery.md): one catalogue selects VM
// images and machine images by OCI manifest digest.

const metadataLimit = 1 << 20
const evidenceLimit = 16 << 20

// catalogueMinimum rejects every catalogue older than the first to carry VM and
// machine images: image workflow run 6. Earlier catalogues carry retired disks.
const catalogueMinimum int64 = 6

var digestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
var imageWord = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._+-]{0,127}$`)
var selectorPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}:[a-z0-9][a-z0-9.+-]{0,63}$`)
var errIncompatibleImage = errors.New("incompatible image")

// imageKind is one kind of catalogue image: its OCI artifact type, its payload
// layer and bounds, and where its verified payload is cached.
type imageKind struct {
	name, artifactType, payload string
	compressedLimit, rawLimit   int64
}

var (
	vmKind      = imageKind{"vm", "application/vnd.frostyard.nsl.vm.v1", "disk.raw.zst", 8 << 30, 32 << 30}
	machineKind = imageKind{"machine", "application/vnd.frostyard.nsl.machine.v1", "rootfs.tar.zst", 4 << 30, 16 << 30}
)

func (k imageKind) layers() map[string]int64 {
	return map[string]int64{"descriptor.json": metadataLimit, "descriptor.sigstore.json": metadataLimit, k.payload: k.compressedLimit,
		"packages.json": evidenceLimit, "provenance.json": evidenceLimit, "acceptance.json": evidenceLimit}
}

type blobRef struct {
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}

func (b blobRef) valid(max int64) bool {
	return digestPattern.MatchString(b.Digest) && b.Size > 0 && b.Size <= max
}

// machineDescriptor is /usr/lib/nsl/machine.json in a machine image
// (docs/specs/machine-images.md#descriptor).
type machineDescriptor struct {
	Schema            int                        `json:"schema"`
	Role              string                     `json:"role"`
	BuildID           string                     `json:"build_id"`
	Distribution      string                     `json:"distribution"`
	Release           string                     `json:"release"`
	Architecture      string                     `json:"architecture"`
	Family            string                     `json:"family"`
	Revision          int                        `json:"revision"`
	MachineProtocol   int                        `json:"machine_protocol"`
	OSID              string                     `json:"os_id"`
	OSVersion         string                     `json:"os_version"`
	Systemd           string                     `json:"systemd"`
	Capabilities      map[string]json.RawMessage `json:"capabilities"`
	IntegrationSHA256 string                     `json:"integration_sha256"`
	RecipesRevision   string                     `json:"recipes_revision"`
	MkosiRevision     string                     `json:"mkosi_revision"`
}

func (d *machineDescriptor) compatible() error {
	if d.Schema != 1 || d.Role != "machine" || d.Architecture != "x86-64" || d.MachineProtocol != protocol.MachineVersion {
		return fmt.Errorf("%w: need a machine image with machine protocol %d on x86-64", errIncompatibleImage, protocol.MachineVersion)
	}
	for _, word := range []string{d.BuildID, d.Distribution, d.Release, d.Family} {
		if !imageWord.MatchString(word) {
			return fmt.Errorf("%w: invalid build identity", errIncompatibleImage)
		}
	}
	return nil
}

// imageArtifact is the signed descriptor.json of an image artifact. It embeds
// the image's own descriptor and records every layer's digest and size.
type imageArtifact struct {
	Schema     int             `json:"schema"`
	Kind       string          `json:"kind"`
	Image      json.RawMessage `json:"image"`
	Raw        *blobRef        `json:"raw,omitempty"`
	Rootfs     *blobRef        `json:"rootfs,omitempty"`
	Compressed blobRef         `json:"compressed"`
	Packages   blobRef         `json:"packages"`
	Provenance blobRef         `json:"provenance"`
	Acceptance blobRef         `json:"acceptance"`
}

// uncompressed is the payload that decompression must produce exactly.
func (d *imageArtifact) uncompressed() blobRef {
	if d.Raw != nil {
		return *d.Raw
	}
	return *d.Rootfs
}

// validate checks the artifact's bounds and its image's compatibility, and
// returns the image's build ID and protocol.
func (d *imageArtifact) validate(k imageKind) (string, int, error) {
	if d.Schema != 1 || d.Kind != k.name || !d.Compressed.valid(k.compressedLimit) || !d.Packages.valid(evidenceLimit) ||
		!d.Provenance.valid(evidenceLimit) || !d.Acceptance.valid(evidenceLimit) {
		return "", 0, errors.New("invalid image artifact schema, kind or bounds")
	}
	if k.name == "vm" {
		var image vmDescriptor
		if d.Raw == nil || d.Rootfs != nil || !d.Raw.valid(k.rawLimit) {
			return "", 0, errors.New("a VM image artifact records its raw disk")
		}
		if err := decodeMetadata(d.Image, &image, metadataLimit); err != nil {
			return "", 0, fmt.Errorf("%w: %v", errIncompatibleImage, err)
		}
		return image.BuildID, image.AgentProtocol, image.compatible()
	}
	var image machineDescriptor
	if d.Rootfs == nil || d.Raw != nil || !d.Rootfs.valid(k.rawLimit) {
		return "", 0, errors.New("a machine image artifact records its root filesystem")
	}
	if err := decodeMetadata(d.Image, &image, metadataLimit); err != nil {
		return "", 0, fmt.Errorf("%w: %v", errIncompatibleImage, err)
	}
	return image.BuildID, image.MachineProtocol, image.compatible()
}

type catalogueEntry struct {
	Kind            string   `json:"kind"`
	Selectors       []string `json:"selectors,omitempty"`
	Architecture    string   `json:"architecture"`
	AgentProtocol   int      `json:"agent_protocol,omitempty"`
	MachineProtocol int      `json:"machine_protocol,omitempty"`
	Manifest        string   `json:"manifest"`
	BuildID         string   `json:"build_id"`
}

// protocol is the entry's agent protocol for a VM image, or its machine
// protocol for a machine image.
func (e catalogueEntry) protocol() int {
	if e.Kind == "vm" {
		return e.AgentProtocol
	}
	return e.MachineProtocol
}

type imageCatalogue struct {
	Schema   int              `json:"schema"`
	Sequence int64            `json:"sequence"`
	Created  time.Time        `json:"created"`
	Expires  time.Time        `json:"expires"`
	Images   []catalogueEntry `json:"images"`
	Revoked  []string         `json:"revoked"`
}

func (c imageCatalogue) validate(now time.Time) error {
	if c.Schema != 1 || c.Sequence < catalogueMinimum || c.Created.IsZero() || c.Created.After(now.Add(5*time.Minute)) || !c.Expires.After(now) || !c.Expires.After(c.Created) || c.Expires.Sub(c.Created) > 30*24*time.Hour {
		return errors.New("catalogue is expired, future-dated, or has unsupported schema/sequence")
	}
	revoked := map[string]bool{}
	for _, d := range c.Revoked {
		if !digestPattern.MatchString(d) || revoked[d] {
			return errors.New("invalid or duplicate revocation")
		}
		revoked[d] = true
	}
	seen := map[string]bool{}
	manifests := map[string]bool{}
	for _, e := range c.Images {
		if !digestPattern.MatchString(e.Manifest) || !imageWord.MatchString(e.BuildID) || !imageWord.MatchString(e.Architecture) || manifests[e.Manifest] || revoked[e.Manifest] {
			return errors.New("invalid, duplicate or revoked catalogue entry")
		}
		manifests[e.Manifest] = true
		switch {
		case e.Kind == "vm" && len(e.Selectors) == 0 && e.AgentProtocol > 0 && e.MachineProtocol == 0:
			// One VM image per architecture and agent protocol.
			key := fmt.Sprintf("vm %s %d", e.Architecture, e.AgentProtocol)
			if seen[key] {
				return errors.New("more than one VM image for an architecture and agent protocol")
			}
			seen[key] = true
		case e.Kind == "machine" && len(e.Selectors) > 0 && e.MachineProtocol > 0 && e.AgentProtocol == 0:
			for _, s := range e.Selectors {
				key := "machine " + e.Architecture + " " + s
				if !selectorPattern.MatchString(s) || seen[key] {
					return errors.New("invalid or duplicate catalogue selector")
				}
				seen[key] = true
			}
		default:
			return errors.New("catalogue entry of unknown kind or with mismatched fields")
		}
	}
	return nil
}

// selectMachine finds the x86-64 machine image for DISTRO:RELEASE, optionally
// pinned to a manifest digest that must still be the current selection.
func selectMachine(cat imageCatalogue, selector string) (catalogueEntry, error) {
	name, pin, hasPin := strings.Cut(selector, "@")
	if !selectorPattern.MatchString(name) || (hasPin && !digestPattern.MatchString(pin)) {
		return catalogueEntry{}, errors.New("expected DISTRO:RELEASE[@sha256:HEX]")
	}
	for _, entry := range cat.Images {
		if entry.Kind != "machine" || entry.Architecture != "x86-64" {
			continue
		}
		for _, candidate := range entry.Selectors {
			if candidate != name {
				continue
			}
			if hasPin && pin != entry.Manifest {
				return catalogueEntry{}, errors.New("pinned image is not the current approved catalogue selection")
			}
			if entry.MachineProtocol != protocol.MachineVersion {
				return catalogueEntry{}, fmt.Errorf("%w: %s needs machine protocol %d; this nsl has %d", errIncompatibleImage, name, entry.MachineProtocol, protocol.MachineVersion)
			}
			return entry, nil
		}
	}
	return catalogueEntry{}, fmt.Errorf("no approved x86-64 machine image for %s; see nsl images", name)
}

// selectVM finds the catalogue's VM image for this CLI's agent protocol.
func selectVM(cat imageCatalogue) (catalogueEntry, error) {
	for _, entry := range cat.Images {
		if entry.Kind == "vm" && entry.Architecture == "x86-64" && entry.AgentProtocol == protocol.Version {
			return entry, nil
		}
	}
	return catalogueEntry{}, fmt.Errorf("the catalogue has no x86-64 VM image for agent protocol %d", protocol.Version)
}

// Signed JSON must have one interpretation: reject duplicate keys at every depth.
func decodeMetadata(data []byte, dst any, max int) error {
	if len(data) > max {
		return errors.New("metadata exceeds size limit")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var value func(int) error
	value = func(depth int) error {
		if depth > 32 {
			return errors.New("metadata nesting limit exceeded")
		}
		token, err := dec.Token()
		if err != nil {
			return err
		}
		if delim, ok := token.(json.Delim); ok {
			switch delim {
			case '{':
				seen := map[string]bool{}
				for dec.More() {
					key, err := dec.Token()
					if err != nil {
						return err
					}
					s, ok := key.(string)
					if !ok || seen[s] {
						return errors.New("duplicate JSON key")
					}
					seen[s] = true
					if err = value(depth + 1); err != nil {
						return err
					}
				}
				end, err := dec.Token()
				if err != nil {
					return err
				}
				if end != json.Delim('}') {
					return errors.New("invalid JSON object")
				}
			case '[':
				for dec.More() {
					if err = value(depth + 1); err != nil {
						return err
					}
				}
				end, err := dec.Token()
				if err != nil {
					return err
				}
				if end != json.Delim(']') {
					return errors.New("invalid JSON array")
				}
			default:
				return errors.New("invalid JSON delimiter")
			}
		}
		return nil
	}
	if err := value(0); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("trailing metadata")
	}
	dec = json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}
