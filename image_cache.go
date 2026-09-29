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
const catalogueRefreshInterval = time.Hour

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
	Document  []byte    `json:"document"`
	Bundle    []byte    `json:"bundle"`
	CheckedAt time.Time `json:"checked_at,omitempty"`
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

type catalogueMode uint8

const (
	catalogueRefresh catalogueMode = iota
	catalogueCached
	catalogueOffline
)

// catalogue returns an authenticated catalogue according to mode and records
// online refreshes before any payload is fetched.
func (c *imageClient) catalogue(mode catalogueMode) (imageCatalogue, error) {
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
	if mode == catalogueOffline {
		if old.Document == nil {
			return empty, errors.New("no verified catalogue cached; run nsl images online first")
		}
		return c.parseCatalogue(old, true)
	}
	now := c.now()
	if mode == catalogueCached && old.Document != nil && !old.CheckedAt.IsZero() && !old.CheckedAt.After(now) && now.Sub(old.CheckedAt) < catalogueRefreshInterval {
		if cat, cacheErr := c.parseCatalogue(old, true); cacheErr == nil {
			return cat, nil
		}
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
	record := catalogueRecord{Document: document, Bundle: signature, CheckedAt: c.now().UTC()}
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

type imageReceipt struct {
	Manifest   []byte `json:"manifest"`
	Descriptor []byte `json:"descriptor"`
	Bundle     []byte `json:"bundle"`
}

// inspectReceipt authenticates an artifact's descriptor against the selected
// manifest and the publisher, and checks it describes the catalogue entry.
func (c *imageClient) inspectReceipt(record imageReceipt, entry catalogueEntry, k imageKind) (imageArtifact, map[string]blobRef, error) {
	var d imageArtifact
	m, err := parseManifest(record.Manifest, entry.Manifest, k.artifactType)
	if err != nil {
		return d, nil, err
	}
	files, err := manifestFiles(m, k.layers())
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
	buildID, version, err := d.validate(k)
	if err != nil {
		return d, nil, err
	}
	if buildID != entry.BuildID || version != entry.protocol() || entry.Kind != k.name {
		return d, nil, errors.New("image does not match its catalogue entry")
	}
	for name, ref := range map[string]blobRef{k.payload: d.Compressed, "packages.json": d.Packages, "provenance.json": d.Provenance, "acceptance.json": d.Acceptance} {
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

// verifiedFile checks a cached payload's owner, mode, size and digest.
func verifiedFile(path string, ref blobRef, uid int) error {
	if err := privateFile(path, uid, 0222); err != nil {
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
		return errors.New("cached image size mismatch")
	}
	h := sha256.New()
	if _, err = io.Copy(h, io.LimitReader(f, ref.Size+1)); err != nil {
		return err
	}
	if "sha256:"+hex.EncodeToString(h.Sum(nil)) != ref.Digest {
		return errors.New("cached image SHA256 mismatch")
	}
	return nil
}

// cachedImage is a verified payload in the image cache: a VM image's raw disk,
// or a machine image's compressed root filesystem.
type cachedImage struct {
	path    string
	ref     blobRef
	buildID string
}

// cacheTarget is where a verified payload lives: VM images as raw disks named
// by their digest, machine images as rootfs.tar.zst named by theirs, in the
// directory VMs read through the image share.
func (c *imageClient) cacheTarget(k imageKind, d imageArtifact) (string, blobRef) {
	if k.name == "vm" {
		return c.app.vmImagePath(strings.TrimPrefix(d.Raw.Digest, "sha256:")), *d.Raw
	}
	return filepath.Join(c.app.machineImages(), strings.TrimPrefix(d.Compressed.Digest, "sha256:")+".tar.zst"), d.Compressed
}

// pullMachine verifies and caches the machine image a selector names.
func (c *imageClient) pullMachine(selector string, offline bool) (*cachedImage, error) {
	return c.pull(machineKind, offline, func(cat imageCatalogue) (catalogueEntry, error) { return selectMachine(cat, selector) })
}

// pullVM verifies and caches the catalogue's VM image.
func (c *imageClient) pullVM(offline bool) (*cachedImage, error) {
	return c.pull(vmKind, offline, selectVM)
}

func (c *imageClient) pull(k imageKind, offline bool, choose func(imageCatalogue) (catalogueEntry, error)) (*cachedImage, error) {
	mode := catalogueRefresh
	if offline {
		mode = catalogueOffline
	}
	cat, err := c.catalogue(mode)
	if err != nil {
		return nil, err
	}
	entry, err := choose(cat)
	if err != nil {
		return nil, err
	}
	image, err := c.fetch(entry, k, offline)
	if err != nil {
		return nil, err
	}
	// A concurrent refresh may withdraw the image while its payload downloads.
	// Recheck the latest locally authenticated policy before returning it.
	if cat, err = c.catalogue(catalogueOffline); err != nil {
		return nil, err
	}
	current, err := choose(cat)
	if err != nil {
		return nil, err
	}
	if current.Manifest != entry.Manifest {
		return nil, errors.New("catalogue selection changed during download; retry")
	}
	return image, nil
}

func (c *imageClient) fetch(entry catalogueEntry, k imageKind, offline bool) (*cachedImage, error) {
	base, err := c.init()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(base, strings.TrimPrefix(entry.Manifest, "sha256:"))
	if err = os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	if err = checkPrivateDir(dir, c.app.uid); err != nil {
		return nil, err
	}
	lock, err := fileLock(filepath.Join(dir, "lock"))
	if err != nil {
		return nil, err
	}
	defer unlock(lock)
	receiptPath := filepath.Join(dir, "receipt.json")
	var record imageReceipt
	if b, readErr := c.read(receiptPath, 5*metadataLimit); readErr == nil {
		if err = decodeMetadata(b, &record, 5*metadataLimit); err != nil {
			return nil, err
		}
	} else {
		if !os.IsNotExist(readErr) || offline {
			return nil, fmt.Errorf("no complete verified image receipt: %w", readErr)
		}
		m, err := c.registry.manifest(entry.Manifest, k.artifactType)
		if err != nil {
			return nil, err
		}
		files, err := manifestFiles(m, k.layers())
		if err != nil {
			return nil, err
		}
		record.Manifest = m.raw
		if record.Descriptor, err = c.registry.metadata(files["descriptor.json"], metadataLimit); err != nil {
			return nil, err
		}
		if record.Bundle, err = c.registry.metadata(files["descriptor.sigstore.json"], metadataLimit); err != nil {
			return nil, err
		}
	}
	d, files, err := c.inspectReceipt(record, entry, k)
	if err != nil {
		return nil, err
	}
	if err = c.evidence(dir, files, offline); err != nil {
		return nil, err
	}
	target, ref := c.cacheTarget(k, d)
	image := &cachedImage{path: target, ref: ref, buildID: entry.BuildID}
	partial := filepath.Join(dir, k.payload+".part")
	// A payload may be shared with another artifact or a local import.
	if err = verifiedFile(target, ref, c.app.uid); err == nil {
		if err = atomicWrite(receiptPath, encodeJSON(record), 0600); err != nil {
			return nil, err
		}
		// A machine image published before an interruption leaves its partial name.
		if err = os.Remove(partial); os.IsNotExist(err) {
			err = nil
		}
		return image, err
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if offline {
		return nil, fmt.Errorf("%s is not cached; retry online", entry.BuildID)
	}
	// An interruption after verification can leave the partial read-only.
	if st, err := os.Lstat(partial); err == nil && st.Mode().IsRegular() && st.Mode().Perm() == 0444 {
		if err = os.Chmod(partial, 0600); err != nil {
			return nil, err
		}
	}
	f, err := os.OpenFile(partial, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if err = privateFile(partial, c.app.uid, 0077); err != nil {
		return nil, err
	}
	if err = c.registry.download(d.Compressed, f, k.compressedLimit, c.app.err); err != nil {
		return nil, err
	}
	fmt.Fprintf(c.app.err, "Verifying %s\n", entry.BuildID)
	var stage *os.File
	if k.name == "vm" {
		// The VM boots from the raw disk, so the cache keeps it decompressed.
		if stage, err = os.CreateTemp(filepath.Dir(target), ".download-*"); err != nil {
			return nil, err
		}
		defer os.Remove(stage.Name())
		defer stage.Close()
	}
	if err = decompressed(f, stage, d.uncompressed()); err != nil {
		return nil, err
	}
	// A machine image stays compressed; the agent verifies it again as it reads.
	published := f
	if stage != nil {
		published = stage
	}
	if err = published.Chmod(0444); err == nil {
		err = published.Sync()
	}
	if err == nil {
		err = c.publish(published.Name(), target, ref)
	}
	if err != nil {
		return nil, err
	}
	if err = atomicWrite(receiptPath, encodeJSON(record), 0600); err != nil {
		return nil, err
	}
	return image, os.Remove(partial)
}

// decompressed streams the verified compressed payload through a bounded
// decoder, proving the decompressed digest and size, and keeps the output in
// stage when one is given.
func decompressed(compressed io.Reader, stage *os.File, want blobRef) error {
	decoder, err := zstd.NewReader(compressed, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(128<<20), zstd.WithDecoderMaxWindow(128<<20))
	if err != nil {
		return err
	}
	defer decoder.Close()
	h := sha256.New()
	out := io.Writer(h)
	if stage != nil {
		out = io.MultiWriter(stage, h)
	}
	n, err := io.Copy(out, io.LimitReader(decoder, want.Size+1))
	if err != nil {
		return fmt.Errorf("decompress image: %w", err)
	}
	if n != want.Size || "sha256:"+hex.EncodeToString(h.Sum(nil)) != want.Digest {
		return errors.New("decompressed image digest/size mismatch")
	}
	return nil
}

// publish links a verified payload into the cache under the manager lock,
// which also serializes local imports; an existing entry must verify.
func (c *imageClient) publish(source, target string, ref blobRef) error {
	manager, err := fileLock(filepath.Join(c.app.home, "lock"))
	if err != nil {
		return err
	}
	defer unlock(manager)
	err = os.Link(source, target)
	if os.IsExist(err) {
		err = verifiedFile(target, ref, c.app.uid)
	}
	if err != nil {
		return err
	}
	return syncDir(filepath.Dir(target))
}
