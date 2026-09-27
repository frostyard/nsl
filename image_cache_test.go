package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

type deliveryFixture struct {
	mu        sync.Mutex
	blobs     map[string][]byte
	manifests map[string][]byte
	cat       imageCatalogue
	artifact  imageArtifact
	raw       []byte
	server    *httptest.Server
	requests  int
}

func refBytes(b []byte) blobRef        { return blobRef{Digest: hashBytes(b), Size: int64(len(b))} }
func fixtureSignature(b []byte) []byte { return []byte(hashBytes(b)) }
func fixtureVerify(b, sig []byte) error {
	if !bytes.Equal(fixtureSignature(b), sig) {
		return errors.New("test signature mismatch")
	}
	return nil
}
func (f *deliveryFixture) manifest(kind string, files map[string][]byte) []byte {
	m := ociManifest{SchemaVersion: 2, MediaType: ociManifestType, ArtifactType: kind, Config: ociLayer{blobRef: refBytes([]byte("{}")), MediaType: "application/vnd.oci.empty.v1+json"}}
	for name, b := range files {
		ref := refBytes(b)
		f.blobs[ref.Digest] = b
		m.Layers = append(m.Layers, ociLayer{blobRef: ref, MediaType: "application/octet-stream", Annotations: map[string]string{"org.opencontainers.image.title": name}})
	}
	b := encodeJSON(m)
	f.manifests[hashBytes(b)] = b
	return b
}
func (f *deliveryFixture) publishImage(compressed []byte) {
	d := f.artifact
	descriptor, _ := json.MarshalIndent(d, "", "  ") // exact signed whitespace must survive receipts
	signature := fixtureSignature(descriptor)
	m := f.manifest(diskArtifactType, map[string][]byte{"descriptor.json": descriptor, "descriptor.sigstore.json": signature, "disk.raw.zst": compressed, "packages.json": []byte("[]"), "provenance.json": []byte("{}"), "acceptance.json": []byte(`{"passed":true}`)})
	f.cat.Images[0].Manifest = hashBytes(m)
	f.publishCatalogue()
}
func (f *deliveryFixture) publishCatalogue() {
	b, _ := json.MarshalIndent(f.cat, "", "  ")
	f.manifests["catalogue-v1"] = f.manifest(catalogueArtifactType, map[string][]byte{"catalogue.json": b, "catalogue.sigstore.json": fixtureSignature(b)})
}
func newDeliveryFixture(t *testing.T) (*deliveryFixture, *imageClient) {
	t.Helper()
	a, _ := testApp(t)
	f := &deliveryFixture{blobs: map[string][]byte{}, manifests: map[string][]byte{}, cat: testCatalogue(), raw: bytes.Repeat([]byte("generic raw image\x00"), 100)}
	encoder, err := zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1))
	if err != nil {
		t.Fatal(err)
	}
	compressed := encoder.EncodeAll(f.raw, nil)
	encoder.Close()
	f.artifact = imageArtifact{Schema: 1, Image: validImageDescriptor(), Raw: refBytes(f.raw), Compressed: refBytes(compressed), Packages: refBytes([]byte("[]")), Provenance: refBytes([]byte("{}")), Acceptance: refBytes([]byte(`{"passed":true}`))}
	f.publishImage(compressed)
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.requests++
		if r.URL.Path == "/token" {
			if r.Header.Get("Authorization") != "" || r.URL.Query().Get("scope") != "repository:"+imageRepository+":pull" {
				t.Error("incorrect anonymous scope or credentials")
			}
			io.WriteString(w, `{"token":"anonymous-test"}`)
			return
		}
		if r.Header.Get("Authorization") != "Bearer anonymous-test" {
			w.WriteHeader(401)
			return
		}
		var b []byte
		if key, ok := strings.CutPrefix(r.URL.Path, "/manifests/"); ok {
			b = f.manifests[key]
		}
		if key, ok := strings.CutPrefix(r.URL.Path, "/blobs/"); ok {
			b = f.blobs[key]
		}
		if b == nil {
			http.NotFound(w, r)
			return
		}
		w.Write(b)
	}))
	t.Cleanup(f.server.Close)
	c := &imageClient{app: a, registry: &imageRegistry{base: f.server.URL, tokenURL: f.server.URL + "/token", client: f.server.Client()}, verify: fixtureVerify, now: time.Now}
	a.imageService = c
	return f, c
}
func TestPullAndOfflineCreate(t *testing.T) {
	f, c := newDeliveryFixture(t)
	path, digest, err := c.pull("debian:13", false)
	if err != nil {
		t.Fatal(err)
	}
	if digest != f.artifact.Raw.Digest {
		t.Fatal("wrong raw digest")
	}
	b, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(b, f.raw) {
		t.Fatalf("wrong cache: %v", err)
	}
	f.server.Close()
	if _, _, err := c.pull("debian:trixie", true); err != nil {
		t.Fatalf("offline receipt/signature round trip: %v", err)
	}
	if err := c.app.execute([]string{"create", "downloaded", "--distro", "debian:13", "--offline"}); err != nil {
		t.Fatal(err)
	}
	e, err := c.app.owned("downloaded")
	if err != nil || e.Digest != strings.TrimPrefix(digest, "sha256:") {
		t.Fatalf("wrong environment: %v", err)
	}
	if _, _, err := c.pull("debian:13", false); err == nil {
		t.Fatal("silent offline fallback")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	b[0] ^= 1
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.pull("debian:13", true); err == nil {
		t.Fatal("accepted corrupted cache")
	}
}
func TestCatalogueRollbackExpiryAndWithdrawal(t *testing.T) {
	f, c := newDeliveryFixture(t)
	if _, _, err := c.pull("debian:13", false); err != nil {
		t.Fatal(err)
	}
	original := f.cat
	for _, sequence := range []int64{9, 10} {
		f.mu.Lock()
		f.cat.Sequence = sequence
		f.cat.Expires = f.cat.Expires.Add(-time.Minute)
		f.publishCatalogue()
		f.mu.Unlock()
		if _, err := c.catalogue(false); err == nil {
			t.Fatal("accepted rollback/equivocation")
		}
	}
	c.now = func() time.Time { return original.Expires }
	if _, _, err := c.pull("debian:13", true); err == nil {
		t.Fatal("accepted expired catalogue offline")
	}
	c.now = time.Now
	f.mu.Lock()
	f.cat = original
	f.cat.Sequence = 11
	f.cat.Revoked = []string{f.cat.Images[0].Manifest}
	f.cat.Images = nil
	f.publishCatalogue()
	f.mu.Unlock()
	if _, _, err := c.pull("debian:13", false); err == nil {
		t.Fatal("accepted withdrawal")
	}
	if _, _, err := c.finishPull("debian:13", original.Images[0], "unused", "unused"); err == nil {
		t.Fatal("in-flight pull ignored withdrawal")
	}
}
func TestPullFailureNeverCreatesEnvironment(t *testing.T) {
	for _, mode := range []string{"signature", "compressed digest", "raw digest", "invalid zstd", "architecture", "symlink", "fifo"} {
		t.Run(mode, func(t *testing.T) {
			f, c := newDeliveryFixture(t)
			compressed := f.blobs[f.artifact.Compressed.Digest]
			switch mode {
			case "signature":
				c.verify = func([]byte, []byte) error { return errors.New("invalid signer") }
			case "compressed digest":
				f.blobs[f.artifact.Compressed.Digest] = bytes.Repeat([]byte("x"), len(compressed))
			case "raw digest":
				f.artifact.Raw.Digest = hashBytes([]byte("wrong"))
				f.publishImage(compressed)
			case "invalid zstd":
				compressed = []byte("not a zstd stream")
				f.artifact.Compressed = refBytes(compressed)
				f.publishImage(compressed)
			case "architecture":
				f.artifact.Image.Architecture = "arm64"
				f.publishImage(compressed)
			case "symlink", "fifo":
				base, err := c.init()
				if err != nil {
					t.Fatal(err)
				}
				dir := filepath.Join(base, strings.TrimPrefix(f.cat.Images[0].Manifest, "sha256:"))
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(dir, "disk.raw.zst.part")
				if mode == "symlink" {
					err = os.Symlink(filepath.Join(t.TempDir(), "victim"), path)
				} else {
					err = syscall.Mkfifo(path, 0600)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := c.app.create("rejected", []string{"--distro", "debian:13"}); err == nil {
				t.Fatal("created invalid image")
			}
			if _, err := os.Lstat(c.app.dir("rejected")); !os.IsNotExist(err) {
				t.Fatalf("named environment remains: %v", err)
			}
			raws, err := filepath.Glob(filepath.Join(c.app.home, "images", "*.raw"))
			if err != nil || len(raws) != 0 {
				t.Fatalf("published invalid raw: %v %v", raws, err)
			}
		})
	}
}
func TestConcurrentPullsShareCompleteCache(t *testing.T) {
	f, c := newDeliveryFixture(t)
	c2 := *c
	c2.registry = &imageRegistry{base: f.server.URL, tokenURL: f.server.URL + "/token", client: f.server.Client()}
	errors := make(chan error, 2)
	for _, client := range []*imageClient{c, &c2} {
		go func(c *imageClient) { _, _, err := c.pull("debian:13", false); errors <- err }(client)
	}
	for range 2 {
		if err := <-errors; err != nil {
			t.Fatal(err)
		}
	}
	raw := filepath.Join(c.app.home, "images", strings.TrimPrefix(f.artifact.Raw.Digest, "sha256:")+".raw")
	if err := verifiedRaw(raw, f.artifact.Raw, c.app.uid); err != nil {
		t.Fatal(err)
	}
}
func TestOfflineCannotFetchMissingData(t *testing.T) {
	f, c := newDeliveryFixture(t)
	if _, err := c.catalogue(false); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	before := f.requests
	f.mu.Unlock()
	if _, _, err := c.pull("debian:13", true); err == nil {
		t.Fatal("accepted incomplete cache")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.requests != before {
		t.Fatal("offline request used network")
	}
}
func TestSignedManifestLayerSet(t *testing.T) {
	f, c := newDeliveryFixture(t)
	entry := f.cat.Images[0]
	var m ociManifest
	if err := json.Unmarshal(f.manifests[entry.Manifest], &m); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"missing", "duplicate", "unknown", "oversized"} {
		t.Run(mode, func(t *testing.T) {
			altered := m
			altered.Layers = append([]ociLayer(nil), m.Layers...)
			switch mode {
			case "missing":
				altered.Layers = altered.Layers[1:]
			case "duplicate":
				altered.Layers = append(altered.Layers, altered.Layers[0])
			case "unknown":
				altered.Layers[0].Annotations = map[string]string{"org.opencontainers.image.title": "../outside"}
			case "oversized":
				altered.Layers[0].Size = rawLimit + 1
			}
			record := imageReceipt{Manifest: encodeJSON(altered)}
			selection := entry
			selection.Manifest = hashBytes(record.Manifest)
			if _, _, err := c.inspectReceipt(record, selection); err == nil {
				t.Fatal("accepted invalid layer set")
			}
		})
	}
}
func TestRegistryDownloadResumeAndValidation(t *testing.T) {
	payload := []byte("0123456789abcdefghij")
	for _, mode := range []string{"range", "ignored range", "bad range", "interrupted", "wrong digest", "oversized", "encoding"} {
		t.Run(mode, func(t *testing.T) {
			file, err := os.CreateTemp(t.TempDir(), "partial")
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			if _, err := file.Write(payload[:5]); err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Range") != "bytes=5-" {
					t.Error("missing resume range")
				}
				switch mode {
				case "ignored range":
					w.Write(payload)
					return
				case "oversized":
					w.(http.Flusher).Flush()
					w.Write(append(payload, 'x'))
					return
				case "encoding":
					w.Header().Set("Content-Encoding", "gzip")
				case "bad range":
					w.Header().Set("Content-Range", "bytes 4-19/20")
					w.WriteHeader(206)
					w.Write(payload[5:])
					return
				}
				w.Header().Set("Content-Range", "bytes 5-19/20")
				w.WriteHeader(206)
				switch mode {
				case "interrupted":
					w.(http.Flusher).Flush()
					w.Write(payload[5:8])
				case "wrong digest":
					w.Write(bytes.Repeat([]byte("x"), len(payload)-5))
				default:
					w.Write(payload[5:])
				}
			}))
			defer server.Close()
			registry := &imageRegistry{base: server.URL, client: server.Client()}
			err = registry.download(refBytes(payload), file, io.Discard)
			if mode == "range" || mode == "ignored range" {
				if err != nil {
					t.Fatal(err)
				}
				got, _ := io.ReadAll(file)
				if !bytes.Equal(got, payload) {
					t.Fatal("wrong resumed bytes")
				}
			} else if err == nil {
				t.Fatal("accepted bad download")
			}
			if mode == "interrupted" {
				st, _ := file.Stat()
				if st.Size() != 8 {
					t.Fatalf("lost resumable bytes: %d", st.Size())
				}
			}
			if mode == "wrong digest" || mode == "oversized" {
				st, _ := file.Stat()
				if st.Size() != 0 {
					t.Fatalf("retained corrupt bytes: %d", st.Size())
				}
			}
		})
	}
}
func TestRegistryRejectsCredentialRedirects(t *testing.T) {
	r := newImageRegistry()
	initial, _ := http.NewRequest("GET", r.base, nil)
	for _, url := range []string{"http://ghcr.io/blob", "https://user:password@ghcr.io/blob"} {
		req, _ := http.NewRequest("GET", url, nil)
		if r.client.CheckRedirect(req, []*http.Request{initial}) == nil {
			t.Fatal("accepted unsafe redirect")
		}
	}
	req, _ := http.NewRequest("GET", "https://cdn.example/blob", nil)
	req.Header.Set("Authorization", "Bearer scoped")
	if err := r.client.CheckRedirect(req, []*http.Request{initial}); err != nil {
		t.Fatal(err)
	}
	if req.Header.Get("Authorization") != "" {
		t.Fatal("leaked repository token")
	}
}
func TestImageCommandValidation(t *testing.T) {
	_, c := newDeliveryFixture(t)
	for i, args := range [][]string{{"--distro", "debian:13", "--image", "file"}, {"--offline"}, {"--distro", "debian:13", "--cpus", "0"}} {
		if err := c.app.create(fmt.Sprintf("bad%d", i), args); err == nil {
			t.Fatal("accepted invalid arguments")
		}
	}
}

// A subprocess isolates the kernel write limit from the test runner and its
// coverage output. This exercises cleanup after a real mid-stream disk write
// failure, followed by a retry once storage can accept the image.
func TestImageCacheWriteFailure(t *testing.T) {
	if os.Getenv("NSL_TEST_FILE_LIMIT") != "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestImageCacheWriteFailure$")
		cmd.Env = append(os.Environ(), "NSL_TEST_FILE_LIMIT=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("write failure subprocess: %v\n%s", err, output)
		}
		return
	}
	f, c := newDeliveryFixture(t)
	f.raw = bytes.Repeat([]byte("image bytes"), 200000)
	encoder, err := zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1))
	if err != nil {
		t.Fatal(err)
	}
	compressed := encoder.EncodeAll(f.raw, nil)
	encoder.Close()
	f.artifact.Raw = refBytes(f.raw)
	f.artifact.Compressed = refBytes(compressed)
	f.publishImage(compressed)
	var original syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &original); err != nil {
		t.Fatal(err)
	}
	limited := original
	limited.Cur = 512 << 10
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &limited); err != nil {
		t.Fatal(err)
	}
	defer syscall.Setrlimit(syscall.RLIMIT_FSIZE, &original)
	err = c.app.create("write-failure", []string{"--distro", "debian:13"})
	if err == nil || !strings.Contains(err.Error(), "decompress image") {
		t.Fatalf("expected disk write error, got %v", err)
	}
	if _, err := os.Lstat(c.app.dir("write-failure")); !os.IsNotExist(err) {
		t.Fatal("failed write published environment")
	}
	paths, err := os.ReadDir(filepath.Join(c.app.home, "images"))
	if err != nil || len(paths) != 0 {
		t.Fatalf("failed write left raw/staging image: %v %v", paths, err)
	}
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &original); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.pull("debian:13", false); err != nil {
		t.Fatalf("storage recovery retry: %v", err)
	}
}
