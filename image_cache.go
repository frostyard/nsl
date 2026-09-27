package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/klauspost/compress/zstd"
)

const catalogueArtifactType = "application/vnd.frostyard.nsl.catalogue.v1"
const diskArtifactType = "application/vnd.frostyard.nsl.image.v1"

type signatureVerifier func([]byte, []byte) error
type imageClient struct {
	app      *app
	registry *imageRegistry
	verify   signatureVerifier
	now      func() time.Time
}

func (a *app) imageClient() *imageClient {
	if a.imageService != nil {
		return a.imageService
	}
	return &imageClient{app: a, registry: newImageRegistry(), verify: verifyImageSignature, now: time.Now}
}
func (c *imageClient) init() (string, error) {
	if err := c.app.init(); err != nil {
		return "", err
	}
	base := filepath.Join(c.app.home, "delivery")
	if err := os.MkdirAll(base, 0700); err != nil {
		return "", err
	}
	if err := checkPrivateDir(base, c.app.uid); err != nil {
		return "", err
	}
	return base, nil
}
func (c *imageClient) read(path string, max int64) ([]byte, error) {
	if err := privateFile(path, c.app.uid, 0022); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return readBounded(f, max)
}

type catalogueRecord struct {
	Document []byte `json:"document"`
	Bundle   []byte `json:"bundle"`
}

func (c *imageClient) parseCatalogue(record catalogueRecord, fresh bool) (imageCatalogue, error) {
	var cat imageCatalogue
	if err := c.verify(record.Document, record.Bundle); err != nil {
		return cat, err
	}
	if err := decodeMetadata(record.Document, &cat, metadataLimit); err != nil {
		return cat, err
	}
	if fresh {
		if err := cat.validate(c.now()); err != nil {
			return cat, err
		}
	}
	return cat, nil
}
func (c *imageClient) catalogue(offline bool) (imageCatalogue, error) {
	var empty imageCatalogue
	base, err := c.init()
	if err != nil {
		return empty, err
	}
	lock, err := fileLock(filepath.Join(base, "catalogue.lock"))
	if err != nil {
		return empty, err
	}
	defer unlock(lock)
	path := filepath.Join(base, "catalogue.json")
	var old catalogueRecord
	var prior imageCatalogue
	if data, err := c.read(path, 3*metadataLimit); err == nil {
		if err = decodeMetadata(data, &old, 3*metadataLimit); err != nil {
			return empty, err
		}
		prior, err = c.parseCatalogue(old, false)
		if err != nil {
			return empty, fmt.Errorf("invalid cached catalogue: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return empty, err
	}
	if offline {
		if old.Document == nil {
			return empty, errors.New("no verified catalogue cached; run nsl images online first")
		}
		return c.parseCatalogue(old, true)
	}
	m, err := c.registry.manifest("catalogue-v1", catalogueArtifactType)
	if err != nil {
		return empty, err
	}
	files, err := manifestFiles(m, map[string]int64{"catalogue.json": metadataLimit, "catalogue.sigstore.json": metadataLimit})
	if err != nil {
		return empty, err
	}
	document, err := c.registry.metadata(files["catalogue.json"], metadataLimit)
	if err != nil {
		return empty, err
	}
	signature, err := c.registry.metadata(files["catalogue.sigstore.json"], metadataLimit)
	if err != nil {
		return empty, err
	}
	record := catalogueRecord{Document: document, Bundle: signature}
	cat, err := c.parseCatalogue(record, true)
	if err != nil {
		return empty, err
	}
	if old.Document != nil && (cat.Sequence < prior.Sequence || (cat.Sequence == prior.Sequence && hashBytes(document) != hashBytes(old.Document))) {
		return empty, errors.New("catalogue rollback or conflicting sequence refused")
	}
	if err = atomicWrite(path, encodeJSON(record), 0600); err != nil {
		return empty, err
	}
	return cat, nil
}
func selectImage(cat imageCatalogue, selector string) (catalogueEntry, error) {
	name, pin, hasPin := strings.Cut(selector, "@")
	if !selectorPattern.MatchString(name) || (hasPin && !digestPattern.MatchString(pin)) {
		return catalogueEntry{}, errors.New("expected DISTRO:RELEASE[@sha256:HEX]")
	}
	for _, entry := range cat.Images {
		if entry.Architecture != "x86-64" {
			continue
		}
		for _, candidate := range entry.Selectors {
			if candidate == name {
				if hasPin && pin != entry.Manifest {
					return catalogueEntry{}, errors.New("pinned image is not the current approved catalogue selection")
				}
				return entry, nil
			}
		}
	}
	return catalogueEntry{}, fmt.Errorf("no approved x86-64 image for %s; use nsl images", name)
}

type imageReceipt struct {
	Manifest   []byte `json:"manifest"`
	Descriptor []byte `json:"descriptor"`
	Bundle     []byte `json:"bundle"`
}

func (c *imageClient) inspectReceipt(record imageReceipt, entry catalogueEntry) (imageArtifact, map[string]blobRef, error) {
	var d imageArtifact
	m, err := parseManifest(record.Manifest, entry.Manifest, diskArtifactType)
	if err != nil {
		return d, nil, err
	}
	files, err := manifestFiles(m, map[string]int64{"descriptor.json": metadataLimit, "descriptor.sigstore.json": metadataLimit, "disk.raw.zst": compressedLimit, "packages.json": evidenceLimit, "provenance.json": evidenceLimit, "acceptance.json": evidenceLimit})
	if err != nil {
		return d, nil, err
	}
	for name, content := range map[string][]byte{"descriptor.json": record.Descriptor, "descriptor.sigstore.json": record.Bundle} {
		if hashBytes(content) != files[name].Digest || int64(len(content)) != files[name].Size {
			return d, nil, errors.New("signed descriptor does not match selected OCI manifest")
		}
	}
	if err = c.verify(record.Descriptor, record.Bundle); err != nil {
		return d, nil, err
	}
	if err = decodeMetadata(record.Descriptor, &d, metadataLimit); err != nil {
		return d, nil, err
	}
	if err = d.validate(); err != nil {
		return d, nil, err
	}
	if d.Image.BuildID != entry.BuildID || d.Image.Architecture != entry.Architecture {
		return d, nil, errors.New("image does not match catalogue identity")
	}
	for name, ref := range map[string]blobRef{"disk.raw.zst": d.Compressed, "packages.json": d.Packages, "provenance.json": d.Provenance, "acceptance.json": d.Acceptance} {
		if files[name] != ref {
			return d, nil, errors.New("payload does not match signed descriptor")
		}
	}
	return d, files, nil
}
func (c *imageClient) evidence(dir string, files map[string]blobRef, offline bool) error {
	for _, name := range []string{"packages.json", "provenance.json", "acceptance.json"} {
		ref := files[name]
		path := filepath.Join(dir, name)
		b, err := c.read(path, ref.Size)
		if err != nil {
			if !os.IsNotExist(err) || offline {
				return fmt.Errorf("missing/invalid cached %s: %w", name, err)
			}
			b, err = c.registry.metadata(ref, evidenceLimit)
			if err != nil {
				return err
			}
			if err = atomicWrite(path, b, 0600); err != nil {
				return err
			}
		}
		if int64(len(b)) != ref.Size || hashBytes(b) != ref.Digest {
			return fmt.Errorf("cached %s digest/size mismatch", name)
		}
	}
	return nil
}
func verifiedRaw(path string, ref blobRef, uid int) error {
	if err := privateFile(path, uid, 0022); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if st.Size() != ref.Size {
		return errors.New("cached raw image size mismatch")
	}
	h := sha256.New()
	if _, err = io.Copy(h, io.LimitReader(f, ref.Size+1)); err != nil {
		return err
	}
	if "sha256:"+hex.EncodeToString(h.Sum(nil)) != ref.Digest {
		return errors.New("cached raw image SHA256 mismatch")
	}
	return nil
}
func (c *imageClient) pull(selector string, offline bool) (string, string, error) {
	cat, err := c.catalogue(offline)
	if err != nil {
		return "", "", err
	}
	entry, err := selectImage(cat, selector)
	if err != nil {
		return "", "", err
	}
	base, err := c.init()
	if err != nil {
		return "", "", err
	}
	dir := filepath.Join(base, strings.TrimPrefix(entry.Manifest, "sha256:"))
	if err = os.MkdirAll(dir, 0700); err != nil {
		return "", "", err
	}
	if err = checkPrivateDir(dir, c.app.uid); err != nil {
		return "", "", err
	}
	lock, err := fileLock(filepath.Join(dir, "lock"))
	if err != nil {
		return "", "", err
	}
	defer unlock(lock)
	receiptPath := filepath.Join(dir, "receipt.json")
	var record imageReceipt
	if b, readErr := c.read(receiptPath, 5*metadataLimit); readErr == nil {
		if err = decodeMetadata(b, &record, 5*metadataLimit); err != nil {
			return "", "", err
		}
	} else {
		if !os.IsNotExist(readErr) || offline {
			return "", "", fmt.Errorf("no complete verified image receipt: %w", readErr)
		}
		m, err := c.registry.manifest(entry.Manifest, diskArtifactType)
		if err != nil {
			return "", "", err
		}
		files, err := manifestFiles(m, map[string]int64{"descriptor.json": metadataLimit, "descriptor.sigstore.json": metadataLimit, "disk.raw.zst": compressedLimit, "packages.json": evidenceLimit, "provenance.json": evidenceLimit, "acceptance.json": evidenceLimit})
		if err != nil {
			return "", "", err
		}
		record.Manifest = m.raw
		record.Descriptor, err = c.registry.metadata(files["descriptor.json"], metadataLimit)
		if err != nil {
			return "", "", err
		}
		record.Bundle, err = c.registry.metadata(files["descriptor.sigstore.json"], metadataLimit)
		if err != nil {
			return "", "", err
		}
	}
	d, files, err := c.inspectReceipt(record, entry)
	if err != nil {
		return "", "", err
	}
	if err = c.evidence(dir, files, offline); err != nil {
		return "", "", err
	}
	raw := filepath.Join(c.app.home, "images", strings.TrimPrefix(d.Raw.Digest, "sha256:")+".raw")
	// A raw digest may be referenced by more than one artifact or a local import.
	// The manager lock serializes final publication with the existing importer.
	if err = verifiedRaw(raw, d.Raw, c.app.uid); err == nil {
		if err = atomicWrite(receiptPath, encodeJSON(record), 0600); err != nil {
			return "", "", err
		}
		return c.finishPull(selector, entry, raw, d.Raw.Digest)
	} else if !os.IsNotExist(err) {
		return "", "", err
	}
	if offline {
		return "", "", errors.New("verified raw image is not cached; retry online")
	}
	partial := filepath.Join(dir, "disk.raw.zst.part")
	f, err := os.OpenFile(partial, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return "", "", err
	}
	defer f.Close()
	if err = privateFile(partial, c.app.uid, 0077); err != nil {
		return "", "", err
	}
	if err = c.registry.download(d.Compressed, f, c.app.err); err != nil {
		return "", "", err
	}
	fmt.Fprintf(c.app.err, "Verifying and decompressing %s\n", entry.BuildID)
	stage, err := os.CreateTemp(filepath.Join(c.app.home, "images"), ".download-*")
	if err != nil {
		return "", "", err
	}
	defer os.Remove(stage.Name())
	defer stage.Close()
	decoder, err := zstd.NewReader(f, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(128<<20), zstd.WithDecoderMaxWindow(128<<20))
	if err != nil {
		return "", "", err
	}
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(stage, h), io.LimitReader(decoder, d.Raw.Size+1))
	decoder.Close()
	if copyErr != nil {
		return "", "", fmt.Errorf("decompress image: %w", copyErr)
	}
	if n != d.Raw.Size || "sha256:"+hex.EncodeToString(h.Sum(nil)) != d.Raw.Digest {
		return "", "", errors.New("decompressed image digest/size mismatch")
	}
	if err = stage.Chmod(0444); err != nil {
		return "", "", err
	}
	if err = stage.Sync(); err != nil {
		return "", "", err
	}
	if err = stage.Close(); err != nil {
		return "", "", err
	}
	manager, err := fileLock(filepath.Join(c.app.home, "lock"))
	if err != nil {
		return "", "", err
	}
	err = os.Link(stage.Name(), raw)
	if os.IsExist(err) {
		err = verifiedRaw(raw, d.Raw, c.app.uid)
	}
	if err == nil {
		folder, e := os.Open(filepath.Dir(raw))
		if e != nil {
			err = e
		} else {
			err = folder.Sync()
			folder.Close()
		}
	}
	unlock(manager)
	if err != nil {
		return "", "", err
	}
	if err = atomicWrite(receiptPath, encodeJSON(record), 0600); err != nil {
		return "", "", err
	}
	if err = os.Remove(partial); err != nil {
		return "", "", err
	}
	return c.finishPull(selector, entry, raw, d.Raw.Digest)
}

// A concurrent refresh may withdraw this image while its payload downloads.
// Recheck the latest locally authenticated policy at the pull's completion.
func (c *imageClient) finishPull(selector string, selected catalogueEntry, path, digest string) (string, string, error) {
	cat, err := c.catalogue(true)
	if err != nil {
		return "", "", err
	}
	current, err := selectImage(cat, selector)
	if err != nil {
		return "", "", err
	}
	if current.Manifest != selected.Manifest {
		return "", "", errors.New("catalogue selection changed during download; retry")
	}
	return path, digest, nil
}
