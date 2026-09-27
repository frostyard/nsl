package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const imageRepository = "frostyard/nsl-images"
const ociManifestType = "application/vnd.oci.image.manifest.v1+json"

type ociLayer struct {
	blobRef
	MediaType   string            `json:"mediaType"`
	Data        []byte            `json:"data,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}
type ociManifest struct {
	raw           []byte
	SchemaVersion int               `json:"schemaVersion"`
	MediaType     string            `json:"mediaType"`
	ArtifactType  string            `json:"artifactType,omitempty"`
	Config        ociLayer          `json:"config"`
	Layers        []ociLayer        `json:"layers"`
	Annotations   map[string]string `json:"annotations,omitempty"`
}
type imageRegistry struct {
	base, tokenURL string
	client         *http.Client
	token          string
}

func newImageRegistry() *imageRegistry {
	return &imageRegistry{base: "https://ghcr.io/v2/" + imageRepository, tokenURL: "https://ghcr.io/token", client: &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, DialContext: (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext, TLSHandshakeTimeout: 15 * time.Second, ResponseHeaderTimeout: 30 * time.Second, MaxResponseHeaderBytes: 1 << 20},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 || req.URL.Scheme != "https" || req.URL.User != nil {
				return errors.New("unsafe registry redirect")
			}
			if req.URL.Host != via[0].URL.Host {
				req.Header.Del("Authorization")
			}
			return nil
		},
	}}
}
func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}
func readBounded(r io.Reader, limit int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, errors.New("response exceeds size limit")
	}
	return b, nil
}
func (r *imageRegistry) authorize(ctx context.Context) error {
	endpoint := r.tokenURL + "?service=ghcr.io&scope=" + url.QueryEscape("repository:"+imageRepository+":pull")
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return err
	}
	response, err := r.client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return fmt.Errorf("anonymous registry authorization: HTTP %d", response.StatusCode)
	}
	b, err := readBounded(response.Body, 65536)
	if err != nil {
		return err
	}
	var token struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err = json.Unmarshal(b, &token); err != nil {
		return err
	}
	if token.Token == "" {
		token.Token = token.AccessToken
	}
	if token.Token == "" || strings.ContainsAny(token.Token, "\r\n") {
		return errors.New("registry returned no valid token")
	}
	r.token = token.Token
	return nil
}
func (r *imageRegistry) get(ctx context.Context, path string, offset int64) (*http.Response, error) {
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequestWithContext(ctx, "GET", r.base+path, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", ociManifestType)
		req.Header.Set("Accept-Encoding", "identity")
		if r.token != "" {
			req.Header.Set("Authorization", "Bearer "+r.token)
		}
		if offset > 0 {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
		}
		resp, err := r.client.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != 401 {
			return resp, nil
		}
		resp.Body.Close()
		if attempt == 0 {
			if err = r.authorize(ctx); err != nil {
				return nil, err
			}
		}
	}
	return nil, errors.New("registry refused anonymous image access")
}
func (r *imageRegistry) manifest(reference, artifactType string) (ociManifest, error) {
	var m ociManifest
	if reference != "catalogue-v1" && !digestPattern.MatchString(reference) {
		return m, errors.New("invalid image manifest reference")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	response, err := r.get(ctx, "/manifests/"+reference, 0)
	if err != nil {
		return m, err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return m, fmt.Errorf("image manifest: HTTP %d", response.StatusCode)
	}
	b, err := readBounded(response.Body, metadataLimit)
	if err != nil {
		return m, err
	}
	return parseManifest(b, reference, artifactType)
}
func parseManifest(b []byte, reference, artifactType string) (ociManifest, error) {
	var m ociManifest
	if reference != "catalogue-v1" && hashBytes(b) != reference {
		return m, errors.New("OCI manifest digest mismatch")
	}
	if err := decodeMetadata(b, &m, metadataLimit); err != nil {
		return m, err
	}
	if m.SchemaVersion != 2 || m.MediaType != ociManifestType || m.ArtifactType != artifactType || len(m.Layers) > 8 {
		return m, errors.New("unsupported OCI image manifest")
	}
	if m.Config.MediaType != "application/vnd.oci.empty.v1+json" || m.Config.Digest != hashBytes([]byte("{}")) || m.Config.Size != 2 || (len(m.Config.Data) > 0 && string(m.Config.Data) != "{}") {
		return m, errors.New("unsupported OCI artifact config")
	}
	m.raw = b
	return m, nil
}
func manifestFiles(m ociManifest, expected map[string]int64) (map[string]blobRef, error) {
	files := map[string]blobRef{}
	for _, layer := range m.Layers {
		name := layer.Annotations["org.opencontainers.image.title"]
		max, ok := expected[name]
		if !ok || !layer.blobRef.valid(max) {
			return nil, errors.New("unexpected or oversized OCI layer")
		}
		if _, ok = files[name]; ok {
			return nil, errors.New("duplicate OCI layer")
		}
		files[name] = layer.blobRef
	}
	if len(files) != len(expected) {
		return nil, errors.New("missing OCI layer")
	}
	return files, nil
}
func (r *imageRegistry) metadata(ref blobRef, max int64) ([]byte, error) {
	if !ref.valid(max) {
		return nil, errors.New("invalid metadata blob")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	response, err := r.get(ctx, "/blobs/"+ref.Digest, 0)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, fmt.Errorf("image metadata: HTTP %d", response.StatusCode)
	}
	b, err := readBounded(response.Body, ref.Size)
	if err != nil {
		return nil, err
	}
	if int64(len(b)) != ref.Size || hashBytes(b) != ref.Digest {
		return nil, errors.New("image metadata digest/size mismatch")
	}
	return b, nil
}

// The caller owns the per-image lock and private staging directory. An interrupted
// transfer remains resumable; a completed blob is always hashed from byte zero.
func (r *imageRegistry) download(ref blobRef, file *os.File, progress io.Writer) error {
	if !ref.valid(compressedLimit) {
		return errors.New("invalid compressed disk bounds")
	}
	st, err := file.Stat()
	if err != nil {
		return err
	}
	offset := st.Size()
	if offset > ref.Size {
		if err = file.Truncate(0); err != nil {
			return err
		}
		offset = 0
	}
	if offset < ref.Size {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		response, err := r.get(ctx, "/blobs/"+ref.Digest, offset)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		switch response.StatusCode {
		case 200:
			if err = file.Truncate(0); err != nil {
				return err
			}
			offset = 0
		case 206:
			// We requested the full remainder, so require the exact returned interval.
			want := fmt.Sprintf("bytes %d-%d/%d", offset, ref.Size-1, ref.Size)
			if offset == 0 || response.Header.Get("Content-Range") != want {
				return errors.New("invalid download range response")
			}
		default:
			return fmt.Errorf("image download: HTTP %d", response.StatusCode)
		}
		if response.Header.Get("Content-Encoding") != "" && response.Header.Get("Content-Encoding") != "identity" {
			return errors.New("unexpected disk HTTP encoding")
		}
		if length := response.Header.Get("Content-Length"); length != "" {
			n, err := strconv.ParseInt(length, 10, 64)
			if err != nil || n != ref.Size-offset {
				return errors.New("disk HTTP length mismatch")
			}
		}
		if _, err = file.Seek(offset, io.SeekStart); err != nil {
			return err
		}
		fmt.Fprintf(progress, "Downloading image: %d / %d bytes\n", offset, ref.Size)
		meter := &downloadProgress{out: progress, current: offset, total: ref.Size, last: time.Now()}
		n, copyErr := io.Copy(io.MultiWriter(file, meter), io.LimitReader(response.Body, ref.Size-offset+1))
		syncErr := file.Sync()
		if n > ref.Size-offset {
			_ = file.Truncate(0)
			return errors.New("download exceeds declared disk size")
		}
		if copyErr != nil {
			return fmt.Errorf("download interrupted; retry to resume: %w", copyErr)
		}
		if syncErr != nil {
			return syncErr
		}
		if n != ref.Size-offset {
			return errors.New("download interrupted; retry to resume")
		}
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	hash := sha256.New()
	if _, err = io.Copy(hash, file); err != nil {
		return err
	}
	if "sha256:"+hex.EncodeToString(hash.Sum(nil)) != ref.Digest {
		_ = file.Truncate(0)
		return errors.New("compressed image SHA256 mismatch")
	}
	_, err = file.Seek(0, io.SeekStart)
	return err
}

type downloadProgress struct {
	out            io.Writer
	current, total int64
	last           time.Time
}

func (p *downloadProgress) Write(b []byte) (int, error) {
	p.current += int64(len(b))
	now := time.Now()
	if now.Sub(p.last) >= 2*time.Second || p.current == p.total {
		fmt.Fprintf(p.out, "Downloading image: %d / %d bytes\n", p.current, p.total)
		p.last = now
	}
	return len(b), nil
}

func encodeJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return append(bytes.TrimSpace(b), '\n')
}
