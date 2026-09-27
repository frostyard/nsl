package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"time"
)

const metadataLimit = 1 << 20
const evidenceLimit = 16 << 20
const compressedLimit int64 = 8 << 30
const rawLimit int64 = 32 << 30
const catalogueMinimum int64 = 1

var digestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
var imageWord = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._+-]{0,127}$`)
var selectorPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}:[a-z0-9][a-z0-9.+-]{0,63}$`)
var errIncompatibleImage = errors.New("incompatible guest image")

type imageDescriptor struct {
	Schema            int                        `json:"schema"`
	BuildID           string                     `json:"build_id"`
	Distribution      string                     `json:"distribution"`
	Release           string                     `json:"release"`
	Architecture      string                     `json:"architecture"`
	Family            string                     `json:"family"`
	Revision          int                        `json:"revision"`
	RootFilesystem    string                     `json:"root_filesystem"`
	OSID              string                     `json:"os_id,omitempty"`
	OSVersion         string                     `json:"os_version,omitempty"`
	Snapshot          string                     `json:"snapshot,omitempty"`
	ProtocolMin       int                        `json:"protocol_min"`
	ProtocolMax       int                        `json:"protocol_max"`
	Transport         string                     `json:"transport"`
	IntegrationSHA256 string                     `json:"integration_sha256"`
	RecipesRevision   string                     `json:"recipes_revision"`
	MkosiRevision     string                     `json:"mkosi_revision"`
	Capabilities      map[string]json.RawMessage `json:"capabilities,omitempty"`
}

func (d imageDescriptor) compatible() error {
	if d.Schema != 1 || d.Architecture != "x86-64" || d.Transport != "nsl-vsock-ssh" || d.ProtocolMin < 1 || d.ProtocolMin > 1 || d.ProtocolMax < 1 || d.ProtocolMax > 65535 {
		return fmt.Errorf("%w: require schema 1, x86-64, nsl-vsock-ssh and command protocol 1", errIncompatibleImage)
	}
	for _, value := range []string{d.BuildID, d.Distribution, d.Release, d.RootFilesystem} {
		if !imageWord.MatchString(value) {
			return fmt.Errorf("%w: invalid build identity", errIncompatibleImage)
		}
	}
	return nil
}

type blobRef struct {
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}

func (b blobRef) valid(max int64) bool {
	return digestPattern.MatchString(b.Digest) && b.Size > 0 && b.Size <= max
}

type imageArtifact struct {
	Schema     int             `json:"schema"`
	Image      imageDescriptor `json:"image"`
	Raw        blobRef         `json:"raw"`
	Compressed blobRef         `json:"compressed"`
	Packages   blobRef         `json:"packages"`
	Provenance blobRef         `json:"provenance"`
	Acceptance blobRef         `json:"acceptance"`
}

func (d imageArtifact) validate() error {
	if d.Schema != 1 || !d.Raw.valid(rawLimit) || !d.Compressed.valid(compressedLimit) || !d.Packages.valid(evidenceLimit) || !d.Provenance.valid(evidenceLimit) || !d.Acceptance.valid(evidenceLimit) {
		return errors.New("invalid image artifact schema or bounds")
	}
	return d.Image.compatible()
}

type catalogueEntry struct {
	Selectors    []string `json:"selectors"`
	Architecture string   `json:"architecture"`
	Manifest     string   `json:"manifest"`
	BuildID      string   `json:"build_id"`
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
		if !digestPattern.MatchString(e.Manifest) || !imageWord.MatchString(e.BuildID) || !imageWord.MatchString(e.Architecture) || len(e.Selectors) == 0 || manifests[e.Manifest] || revoked[e.Manifest] {
			return errors.New("invalid, duplicate or revoked catalogue entry")
		}
		manifests[e.Manifest] = true
		for _, s := range e.Selectors {
			key := e.Architecture + ":" + s
			if !selectorPattern.MatchString(s) || seen[key] {
				return errors.New("invalid or duplicate catalogue selector")
			}
			seen[key] = true
		}
	}
	return nil
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
