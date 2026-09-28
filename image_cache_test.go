package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

	"github.com/frostyard/nsl/internal/protocol"
	"github.com/klauspost/compress/zstd"
)

// fixtureImage is one published image: its artifact descriptor and payload.
type fixtureImage struct {
	kind       imageKind
	index      int // its catalogue entry
	artifact   imageArtifact
	payload    []byte
	compressed []byte
}

type deliveryFixture struct {
	mu          sync.Mutex
	blobs       map[string][]byte
	manifests   map[string][]byte
	cat         imageCatalogue
	vm, machine *fixtureImage
	server      *httptest.Server
	requests    int
}

func refBytes(b []byte) blobRef        { return blobRef{Digest: hashBytes(b), Size: int64(len(b))} }
func fixtureSignature(b []byte) []byte { return []byte(hashBytes(b)) }
func fixtureVerify(b, sig []byte) error {
	if !bytes.Equal(fixtureSignature(b), sig) {
		return errors.New("test signature mismatch")
	}
	return nil
}

func compress(t testing.TB, b []byte) []byte {
	encoder, err := zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1))
	if err != nil {
		t.Fatal(err)
	}
	defer encoder.Close()
	return encoder.EncodeAll(b, nil)
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

// publish signs an image's descriptor, publishes its artifact and points its
// catalogue entry at it.
func (f *deliveryFixture) publish(img *fixtureImage) {
	descriptor, _ := json.MarshalIndent(img.artifact, "", "  ") // exact signed whitespace must survive receipts
	m := f.manifest(img.kind.artifactType, map[string][]byte{"descriptor.json": descriptor, "descriptor.sigstore.json": fixtureSignature(descriptor),
		img.kind.payload: img.compressed, "packages.json": []byte("[]"), "provenance.json": []byte("{}"), "acceptance.json": []byte(`{"passed":true}`)})
	f.cat.Images[img.index].Manifest = hashBytes(m)
	f.publishCatalogue()
}

func (f *deliveryFixture) publishCatalogue() {
	b, _ := json.MarshalIndent(f.cat, "", "  ")
	f.manifests["catalogue-v1"] = f.manifest(catalogueArtifactType, map[string][]byte{"catalogue.json": b, "catalogue.sigstore.json": fixtureSignature(b)})
}

func fixtureArtifact(t testing.TB, k imageKind, index int, descriptor string, payload []byte) *fixtureImage {
	compressed := compress(t, payload)
	img := &fixtureImage{kind: k, index: index, payload: payload, compressed: compressed, artifact: imageArtifact{Schema: 1, Kind: k.name,
		Image: json.RawMessage(descriptor), Compressed: refBytes(compressed), Packages: refBytes([]byte("[]")), Provenance: refBytes([]byte("{}")),
		Acceptance: refBytes([]byte(`{"passed":true}`))}}
	ref := refBytes(payload)
	if k.name == "vm" {
		img.artifact.Raw = &ref
	} else {
		img.artifact.Rootfs = &ref
	}
	return img
}

// setPayload replaces an image's payload and republishes it.
func (f *deliveryFixture) setPayload(t testing.TB, img *fixtureImage, payload []byte) {
	img.payload, img.compressed = payload, compress(t, payload)
	img.artifact.Compressed = refBytes(img.compressed)
	ref := refBytes(payload)
	if img.kind.name == "vm" {
		img.artifact.Raw = &ref
	} else {
		img.artifact.Rootfs = &ref
	}
	f.publish(img)
}

func newDeliveryFixture(t *testing.T) (*deliveryFixture, *imageClient, *fakeRunner) {
	t.Helper()
	a, runner := testApp(t)
	f := &deliveryFixture{blobs: map[string][]byte{}, manifests: map[string][]byte{}, cat: testCatalogue()}
	f.vm = fixtureArtifact(t, vmKind, 0, testVMDescriptor, bytes.Repeat([]byte("generic raw image\x00"), 100))
	f.machine = fixtureArtifact(t, machineKind, 1, testMachineDescriptor, bytes.Repeat([]byte("machine rootfs\x00"), 100))
	f.publish(f.vm)
	f.publish(f.machine)
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
	return f, c, runner
}

func TestPullMachineAndOffline(t *testing.T) {
	f, c, _ := newDeliveryFixture(t)
	image, err := c.pullMachine("debian:13", false)
	if err != nil {
		t.Fatal(err)
	}
	// Machine images stay compressed, in the directory VMs read.
	want := filepath.Join(c.app.machineImages(), strings.TrimPrefix(f.machine.artifact.Compressed.Digest, "sha256:")+".tar.zst")
	if image.path != want || image.ref != f.machine.artifact.Compressed || image.buildID != "nsl-machine-debian-trixie-x86-64-r1" {
		t.Fatalf("%+v", image)
	}
	b, err := os.ReadFile(image.path)
	if st, _ := os.Stat(image.path); err != nil || !bytes.Equal(b, f.machine.compressed) || st.Mode().Perm() != 0444 {
		t.Fatalf("wrong cache: %v", err)
	}
	if entries, _ := os.ReadDir(c.app.machineImages()); len(entries) != 1 {
		t.Fatal("the image share holds more than verified images:", entries)
	}
	f.server.Close()
	if _, err := c.pullMachine("debian:trixie", true); err != nil {
		t.Fatalf("offline receipt/signature round trip: %v", err)
	}
	if _, err := c.pullMachine("debian:13", false); err == nil {
		t.Fatal("silent offline fallback")
	}
	os.Chmod(image.path, 0600)
	b[0] ^= 1
	os.WriteFile(image.path, b, 0600)
	os.Chmod(image.path, 0444)
	if _, err := c.pullMachine("debian:13", true); err == nil {
		t.Fatal("accepted corrupted cache")
	}
}

func TestUpdateAndCreateFromTheCatalogue(t *testing.T) {
	f, c, runner := newDeliveryFixture(t)
	a := c.app
	var out bytes.Buffer
	a.out = &out
	// With no VM image selected, create fetches the catalogue's.
	if err := a.execute([]string{"create", "debian", "--distro", "debian:13"}); err != nil {
		t.Fatal(err)
	}
	v, err := a.loadVM()
	if err != nil || v.Image != strings.TrimPrefix(f.vm.artifact.Raw.Digest, "sha256:") {
		t.Fatal(err, v)
	}
	if raw, err := os.ReadFile(a.vmImagePath(v.Image)); err != nil || !bytes.Equal(raw, f.vm.payload) {
		t.Fatal("the VM image cache holds the wrong bytes:", err)
	}
	create := runner.requests[len(runner.requests)-1]
	hex := strings.TrimPrefix(f.machine.artifact.Compressed.Digest, "sha256:")
	if create.Op != "create" || create.Image.Path != protocol.ImageShare+"/"+hex+".tar.zst" || create.Image.Digest != f.machine.artifact.Compressed.Digest ||
		create.Image.Size != f.machine.artifact.Compressed.Size || create.Image.BuildID != "nsl-machine-debian-trixie-x86-64-r1" {
		t.Fatalf("%+v %+v", create, create.Image)
	}
	if m, err := a.machine("debian"); err != nil || m.Image != hex {
		t.Fatal(err, m)
	}
	// A newer VM image in the catalogue waits for update, then for the next start.
	f.mu.Lock()
	f.cat.Sequence++
	f.setPayload(t, f.vm, bytes.Repeat([]byte("newer raw image\x00"), 100))
	f.mu.Unlock()
	if err := a.execute([]string{"create", "fedora", "--distro", "debian:trixie"}); err != nil {
		t.Fatal(err)
	}
	if v, _ = a.loadVM(); v.PendingImage != "" {
		t.Fatal("create replaced a selected VM image")
	}
	if err := a.execute([]string{"update"}); err != nil {
		t.Fatal(err)
	}
	if v, _ = a.loadVM(); v.PendingImage != strings.TrimPrefix(f.vm.artifact.Raw.Digest, "sha256:") {
		t.Fatalf("%+v", v)
	}
	out.Reset()
	if err := a.execute([]string{"images", "--offline"}); err != nil {
		t.Fatal(err)
	}
	listing := strings.Join(strings.Fields(out.String()), " ")
	for _, want := range []string{"Catalogue 11", "vm - nsl-vm-trixie-x86-64-r1 yes", "machine debian:trixie, debian:13 nsl-machine-debian-trixie-x86-64-r1 yes", "VM image: sha256:"} {
		if !strings.Contains(listing, want) {
			t.Fatalf("missing %q in\n%s", want, out.String())
		}
	}
	for _, args := range [][]string{
		{"create", "x", "--distro", "debian:13", "--image", "f", "--digest", hashBytes(nil)},
		{"create", "x", "--offline", "--image", "f", "--digest", hashBytes(nil)},
		{"create", "x"},
		{"update", "--offline", "--image", "f", "--digest", hashBytes(nil)},
	} {
		if err := a.execute(args); err == nil {
			t.Fatal("accepted", args)
		}
	}
}

func TestCatalogueRollbackExpiryAndWithdrawal(t *testing.T) {
	f, c, _ := newDeliveryFixture(t)
	if _, err := c.pullMachine("debian:13", false); err != nil {
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
	if _, err := c.pullMachine("debian:13", true); err == nil {
		t.Fatal("accepted expired catalogue offline")
	}
	c.now = time.Now
	f.mu.Lock()
	f.cat = original
	f.cat.Sequence = 11
	f.cat.Revoked = []string{f.cat.Images[1].Manifest}
	f.cat.Images = f.cat.Images[:1]
	f.publishCatalogue()
	f.mu.Unlock()
	if _, err := c.pullMachine("debian:13", false); err == nil {
		t.Fatal("accepted withdrawal")
	}
	// A pull that selected the image before the withdrawal checks again before returning.
	selections := 0
	if _, err := c.pull(machineKind, true, func(cat imageCatalogue) (catalogueEntry, error) {
		if selections++; selections == 1 {
			return original.Images[1], nil
		}
		return selectMachine(cat, "debian:13")
	}); err == nil || !strings.Contains(err.Error(), "no approved") {
		t.Fatal("in-flight pull ignored withdrawal:", err)
	}
}

func TestPullFailureNeverPublishes(t *testing.T) {
	for _, mode := range []string{"signature", "compressed digest", "rootfs digest", "invalid zstd", "architecture", "protocol", "kind", "symlink", "fifo"} {
		t.Run(mode, func(t *testing.T) {
			f, c, _ := newDeliveryFixture(t)
			img := f.machine
			switch mode {
			case "signature":
				c.verify = func([]byte, []byte) error { return errors.New("invalid signer") }
			case "compressed digest":
				f.blobs[img.artifact.Compressed.Digest] = bytes.Repeat([]byte("x"), len(img.compressed))
			case "rootfs digest":
				wrong := refBytes([]byte("wrong"))
				wrong.Size = img.artifact.Rootfs.Size
				img.artifact.Rootfs = &wrong
				f.publish(img)
			case "invalid zstd":
				img.compressed = []byte("not a zstd stream")
				img.artifact.Compressed = refBytes(img.compressed)
				f.publish(img)
			case "architecture":
				img.artifact.Image = replace(testMachineDescriptor, `"x86-64"`, `"arm64"`)
				f.publish(img)
			case "protocol":
				img.artifact.Image = replace(testMachineDescriptor, `"machine_protocol":1`, `"machine_protocol":2`)
				f.publish(img)
			case "kind":
				// The VM image's artifact behind the machine's catalogue entry.
				f.cat.Images[1].Manifest = f.cat.Images[0].Manifest
				f.cat.Images[0].Manifest = hashBytes([]byte("elsewhere"))
				f.publishCatalogue()
			case "symlink", "fifo":
				base, err := c.init()
				if err != nil {
					t.Fatal(err)
				}
				dir := filepath.Join(base, strings.TrimPrefix(f.cat.Images[1].Manifest, "sha256:"))
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(dir, "rootfs.tar.zst.part")
				if mode == "symlink" {
					err = os.Symlink(filepath.Join(t.TempDir(), "victim"), path)
				} else {
					err = syscall.Mkfifo(path, 0600)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err := c.pullMachine("debian:13", false); err == nil {
				t.Fatal("pulled an invalid image")
			}
			for _, dir := range []string{c.app.machineImages(), filepath.Join(c.app.home, "images", "vm")} {
				if entries, _ := os.ReadDir(dir); len(entries) != 0 {
					t.Fatalf("published an invalid image: %v", entries)
				}
			}
		})
	}
}

func TestPullResumesAfterAnInterruptedPublication(t *testing.T) {
	f, c, _ := newDeliveryFixture(t)
	base, err := c.init()
	if err != nil {
		t.Fatal(err)
	}
	// Verified and made read-only, but never linked into the cache.
	dir := filepath.Join(base, strings.TrimPrefix(f.cat.Images[1].Manifest, "sha256:"))
	os.Mkdir(dir, 0700)
	partial := filepath.Join(dir, "rootfs.tar.zst.part")
	os.WriteFile(partial, f.machine.compressed, 0444)
	image, err := c.pullMachine("debian:13", false)
	if err != nil {
		t.Fatal(err)
	}
	if err = verifiedFile(image.path, f.machine.artifact.Compressed, c.app.uid); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Lstat(partial); !os.IsNotExist(err) {
		t.Fatal("kept the partial download")
	}
	// Linked into the cache, but the partial name and the receipt remain undone.
	os.Link(image.path, partial)
	os.Remove(filepath.Join(dir, "receipt.json"))
	if _, err = c.pullMachine("debian:13", false); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Lstat(partial); !os.IsNotExist(err) {
		t.Fatal("kept the partial name of a published image")
	}
}

func TestConcurrentPullsShareCompleteCache(t *testing.T) {
	f, c, _ := newDeliveryFixture(t)
	c2 := *c
	c2.registry = &imageRegistry{base: f.server.URL, tokenURL: f.server.URL + "/token", client: f.server.Client()}
	results := make(chan error, 4)
	for _, client := range []*imageClient{c, &c2} {
		go func(c *imageClient) { _, err := c.pullMachine("debian:13", false); results <- err }(client)
		go func(c *imageClient) { _, err := c.pullVM(false); results <- err }(client)
	}
	for range 4 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if err := verifiedFile(c.app.vmImagePath(strings.TrimPrefix(f.vm.artifact.Raw.Digest, "sha256:")), *f.vm.artifact.Raw, c.app.uid); err != nil {
		t.Fatal(err)
	}
}

func TestOfflineCannotFetchMissingData(t *testing.T) {
	f, c, _ := newDeliveryFixture(t)
	if _, err := c.catalogue(false); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	before := f.requests
	f.mu.Unlock()
	if _, err := c.pullMachine("debian:13", true); err == nil {
		t.Fatal("accepted incomplete cache")
	}
	if _, err := c.pullVM(true); err == nil {
		t.Fatal("accepted incomplete cache")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.requests != before {
		t.Fatal("offline request used network")
	}
}

func TestSignedManifestLayerSet(t *testing.T) {
	f, c, _ := newDeliveryFixture(t)
	entry := f.cat.Images[1]
	var m ociManifest
	if err := json.Unmarshal(f.manifests[entry.Manifest], &m); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"missing", "duplicate", "unknown", "oversized", "vm payload"} {
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
				altered.Layers[0].Size = machineKind.rawLimit + 1
			case "vm payload":
				for i, layer := range altered.Layers {
					if layer.Annotations["org.opencontainers.image.title"] == "rootfs.tar.zst" {
						altered.Layers[i].Annotations = map[string]string{"org.opencontainers.image.title": "disk.raw.zst"}
					}
				}
			}
			record := imageReceipt{Manifest: encodeJSON(altered)}
			selection := entry
			selection.Manifest = hashBytes(record.Manifest)
			if _, _, err := c.inspectReceipt(record, selection, machineKind); err == nil {
				t.Fatal("accepted invalid layer set")
			}
		})
	}
}

func TestRegistryDownloadResumeAndValidation(t *testing.T) {
	payload := []byte("0123456789abcdefghij")
	for _, mode := range []string{"range", "ignored range", "bad range", "interrupted", "wrong digest", "oversized", "encoding", "limit"} {
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
			limit := int64(len(payload))
			if mode == "limit" {
				limit--
			}
			err = registry.download(refBytes(payload), file, limit, io.Discard)
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
	_, c, _ := newDeliveryFixture(t)
	for _, args := range [][]string{{"pull"}, {"images", "extra"}, {"pull", "debian:13", "extra"}, {"images", "--cpus", "2"}, {"pull", "--offline"}} {
		if err := c.app.imageCommand(args); err == nil {
			t.Fatal("accepted invalid arguments", args)
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
	f, c, _ := newDeliveryFixture(t)
	f.setPayload(t, f.vm, bytes.Repeat([]byte("image bytes"), 200000))
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
	_, err := c.pullVM(false)
	if err == nil || !strings.Contains(err.Error(), "decompress image") {
		t.Fatalf("expected disk write error, got %v", err)
	}
	if entries, _ := os.ReadDir(filepath.Join(c.app.home, "images", "vm")); len(entries) != 0 {
		t.Fatalf("failed write left raw/staging image: %v", entries)
	}
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &original); err != nil {
		t.Fatal(err)
	}
	if _, err := c.pullVM(false); err != nil {
		t.Fatalf("storage recovery retry: %v", err)
	}
}
