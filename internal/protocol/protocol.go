// Package protocol is the request format shared by the nsl CLI and the VM agent.
// See docs/specs/agent.md. Both ends are built from this repository, so the
// version detects mismatched builds and is never negotiated.
package protocol

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"
)

// Version is the agent protocol; MachineVersion is the machine-layer protocol
// that the agent requires of machine images.
const (
	Version        = 1
	MachineVersion = 1
	Limit          = 64 << 10
	AgentPath      = "/usr/lib/nsl/nsl-agent"
	// ImageShare is where a VM mounts the host's verified machine-image cache.
	ImageShare = "/var/cache/nsl/images"
	// ArchiveLimit bounds an exported root filesystem, compressed or not: the
	// largest data disk.
	ArchiveLimit int64 = 4096 << 30
)

var (
	namePattern    = regexp.MustCompile(`^[a-z][a-z0-9-]{0,23}$`)
	idPattern      = regexp.MustCompile(`^[0-9a-f]{32}$`)
	accountPattern = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
	envPattern     = regexp.MustCompile(`^(TERM|COLORTERM|LANG|LANGUAGE|LC_[A-Z]+)$`)
	digestPattern  = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	buildPattern   = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._+-]{0,127}$`)
	zonePattern    = regexp.MustCompile(`^[A-Za-z0-9_+-]+(/[A-Za-z0-9_+-]+)*$`)
	imageFile      = regexp.MustCompile(`^[0-9a-f]{64}\.tar\.zst$`)
)

// ValidName reports whether s is a machine name: a lowercase letter, then
// lowercase letters, digits or interior hyphens, at most 24 characters.
func ValidName(s string) bool { return namePattern.MatchString(s) && !strings.HasSuffix(s, "-") }

// ValidID reports whether s is a 32-hex VM or machine ID.
func ValidID(s string) bool { return idPattern.MatchString(s) }

// ValidAccount reports whether s is a POSIX account name nsl accepts.
func ValidAccount(s string) bool { return accountPattern.MatchString(s) }

// ValidZone reports whether s is an IANA zone name without path tricks.
func ValidZone(s string) bool { return zonePattern.MatchString(s) && !strings.Contains(s, "..") }

// ValidUID reports whether n is usable as a machine account's UID or GID.
func ValidUID(n int) bool { return n >= 1 && n <= 2147483646 && n != 65534 }

type Account struct {
	User  string `json:"user"`
	Group string `json:"group"`
	UID   int    `json:"uid"`
	GID   int    `json:"gid"`
}

func (a *Account) validate() error {
	if !ValidAccount(a.User) || !ValidAccount(a.Group) || !ValidUID(a.UID) || !ValidUID(a.GID) {
		return errors.New("invalid account")
	}
	return nil
}

// Image names a verified machine image in the VM's read-only image share.
// BuildID is empty for a local image; the agent reports the descriptor's.
type Image struct {
	Path    string `json:"path"`
	Digest  string `json:"digest"`
	Size    int64  `json:"size"`
	BuildID string `json:"build_id"`
}

func (i *Image) validate() error {
	dir, file := path.Split(i.Path)
	if dir != ImageShare+"/" || !imageFile.MatchString(file) || !digestPattern.MatchString(i.Digest) ||
		"sha256:"+strings.TrimSuffix(file, ".tar.zst") != i.Digest || i.Size < 1 || (i.BuildID != "" && !buildPattern.MatchString(i.BuildID)) {
		return errors.New("invalid image")
	}
	return nil
}

// Rootfs is the compressed root filesystem an import reads from stdin, as the
// archive's manifest records it.
type Rootfs struct {
	Digest  string `json:"digest"`
	Size    int64  `json:"size"`
	BuildID string `json:"build_id"`
}

func (r *Rootfs) validate() error {
	if !digestPattern.MatchString(r.Digest) || r.Size < 1 || r.Size > ArchiveLimit || !buildPattern.MatchString(r.BuildID) {
		return errors.New("invalid root filesystem")
	}
	return nil
}

type Request struct {
	Protocol    int               `json:"protocol"`
	Op          string            `json:"op"`
	Machine     string            `json:"machine,omitempty"`
	ID          string            `json:"id,omitempty"`
	Argv        []string          `json:"argv,omitempty"`
	Directory   string            `json:"directory,omitempty"`
	Root        bool              `json:"root,omitempty"`
	TTY         bool              `json:"tty,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
	IdleTimeout *int              `json:"idle_timeout,omitempty"`
	Account     *Account          `json:"account,omitempty"`
	Image       *Image            `json:"image,omitempty"`
	Rootfs      *Rootfs           `json:"rootfs,omitempty"`
	TimeZone    string            `json:"time_zone,omitempty"`
}

// Fields each operation takes beyond protocol and op. Anything else is refused.
var operations = map[string][]string{
	"identity": {},
	"machines": {},
	"vm":       {"argv"},
	"start":    {"machine", "id", "idle_timeout"},
	"stop":     {"machine", "id"},
	"run":      {"machine", "id", "argv", "directory", "root", "tty", "env", "idle_timeout"},
	"create":   {"machine", "id", "account", "image", "time_zone"},
	"import":   {"machine", "id", "account", "rootfs", "time_zone"},
	"export":   {"machine", "id"},
	"remove":   {"machine", "id"},
}

// Fields a request must carry for its operation.
var required = map[string]bool{"machine": true, "id": true, "argv": true, "idle_timeout": true, "account": true, "image": true, "rootfs": true, "time_zone": true}

func (r *Request) present() map[string]bool {
	return map[string]bool{
		"machine": r.Machine != "", "id": r.ID != "", "argv": r.Argv != nil, "directory": r.Directory != "",
		"root": r.Root, "tty": r.TTY, "env": r.Env != nil, "idle_timeout": r.IdleTimeout != nil,
		"account": r.Account != nil, "image": r.Image != nil, "rootfs": r.Rootfs != nil, "time_zone": r.TimeZone != "",
	}
}

// Validate checks a request completely before anything acts on it.
func (r *Request) Validate() error {
	if r.Protocol != Version {
		return &Error{CodeProtocol, fmt.Sprintf("request protocol %d, agent protocol %d", r.Protocol, Version)}
	}
	fields, ok := operations[r.Op]
	if !ok {
		return badRequest("unknown operation %q", r.Op)
	}
	allowed := map[string]bool{}
	for _, f := range fields {
		allowed[f] = true
	}
	for field, set := range r.present() {
		if set && !allowed[field] {
			return badRequest("%s does not take %s", r.Op, field)
		}
		if !set && allowed[field] && required[field] {
			return badRequest("%s requires %s", r.Op, field)
		}
	}
	if allowed["machine"] && !ValidName(r.Machine) {
		return badRequest("invalid machine name")
	}
	if allowed["id"] && !ValidID(r.ID) {
		return badRequest("invalid machine ID")
	}
	if allowed["argv"] {
		if len(r.Argv) == 0 {
			return badRequest("empty argv")
		}
		for _, a := range r.Argv {
			if strings.IndexByte(a, 0) >= 0 {
				return badRequest("NUL in argv")
			}
		}
	}
	if r.Directory != "" && (!path.IsAbs(r.Directory) || strings.IndexByte(r.Directory, 0) >= 0 || len(r.Directory) > 4096) {
		return badRequest("directory must be an absolute path")
	}
	for k, v := range r.Env {
		if !envPattern.MatchString(k) || len(v) > 256 || strings.ContainsAny(v, "\x00\n") {
			return badRequest("environment variable %q is not allowed", k)
		}
	}
	if r.IdleTimeout != nil && (*r.IdleTimeout < 0 || *r.IdleTimeout > 1440) {
		return badRequest("idle_timeout must be 0 to 1440 minutes")
	}
	if r.Account != nil {
		if err := r.Account.validate(); err != nil {
			return badRequest("%v", err)
		}
	}
	if r.Image != nil {
		if err := r.Image.validate(); err != nil {
			return badRequest("%v", err)
		}
	}
	if r.Rootfs != nil {
		if err := r.Rootfs.validate(); err != nil {
			return badRequest("%v", err)
		}
	}
	if allowed["time_zone"] && !ValidZone(r.TimeZone) {
		return badRequest("invalid time zone")
	}
	return nil
}

// Encode validates a request and frames it for SSH_ORIGINAL_COMMAND.
func Encode(r Request) (string, error) {
	if err := r.Validate(); err != nil {
		return "", err
	}
	b, err := json.Marshal(r)
	if err != nil {
		return "", err
	}
	if len(b) > Limit {
		return "", errors.New("request exceeds 64 KiB")
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

// Decode reverses Encode, refusing anything with more than one interpretation.
func Decode(s string) (*Request, error) {
	if len(s) > base64.StdEncoding.EncodedLen(Limit) {
		return nil, badRequest("request exceeds 64 KiB")
	}
	b, err := base64.StdEncoding.Strict().DecodeString(s)
	if err != nil {
		return nil, badRequest("request is not base64")
	}
	var r Request
	if err = DecodeStrict(b, &r); err != nil {
		return nil, badRequest("%v", err)
	}
	if err = r.Validate(); err != nil {
		return nil, err
	}
	return &r, nil
}

// DecodeStrict decodes one JSON value, rejecting duplicate keys, unknown fields
// and trailing data.
func DecodeStrict(data []byte, dst any) error {
	if err := uniqueKeys(json.NewDecoder(bytes.NewReader(data)), 0); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("trailing data")
	}
	return nil
}

func uniqueKeys(dec *json.Decoder, depth int) error {
	if depth > 32 {
		return errors.New("nesting limit exceeded")
	}
	token, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for dec.More() {
			key, err := dec.Token()
			if err != nil {
				return err
			}
			if seen[key.(string)] {
				return fmt.Errorf("duplicate key %q", key)
			}
			seen[key.(string)] = true
			if err = uniqueKeys(dec, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for dec.More() {
			if err := uniqueKeys(dec, depth+1); err != nil {
				return err
			}
		}
	}
	_, err = dec.Token()
	return err
}

// Error codes from docs/specs/agent.md.
const (
	CodeBadRequest     = "bad-request"
	CodeProtocol       = "protocol"
	CodeUnknownMachine = "unknown-machine"
	CodeMachineID      = "machine-id"
	CodeNotRunning     = "not-running"
	CodeBusy           = "busy"
	CodeRefused        = "refused"
	CodeFailed         = "failed"
)

// ErrorExit is the agent's exit status for its own errors.
const ErrorExit = 255

type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return "nsl-agent: " + e.Code + ": " + e.Message }

func badRequest(format string, args ...any) error {
	return &Error{CodeBadRequest, fmt.Sprintf(format, args...)}
}

// ParseError recovers an agent error from its stderr line, for operations whose
// stderr belongs to the agent rather than to a user command.
func ParseError(stderr string) *Error {
	for _, line := range strings.Split(stderr, "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "nsl-agent: ")
		if !ok {
			continue
		}
		if code, message, ok := strings.Cut(rest, ": "); ok {
			return &Error{code, message}
		}
	}
	return nil
}

// Identity is the agent's answer to the identity operation.
type Identity struct {
	Protocol int             `json:"protocol"`
	VM       Binding         `json:"vm"`
	Image    json.RawMessage `json:"image"`
}

// Binding is the VM identity recorded on first boot and checked on every boot.
type Binding struct {
	Version int         `json:"version"`
	ID      string      `json:"id"`
	Role    string      `json:"role"`
	UID     int         `json:"uid"`
	GID     int         `json:"gid"`
	Machine *MachineRef `json:"machine,omitempty"`
}

type MachineRef struct {
	Name string `json:"name"`
	ID   string `json:"id"`
}

// Validate checks a binding's fields and the role's machine reference.
func (b *Binding) Validate() error {
	if b.Version != 1 || !ValidID(b.ID) || !ValidUID(b.UID) || !ValidUID(b.GID) {
		return errors.New("invalid VM binding")
	}
	switch b.Role {
	case "shared":
		if b.Machine != nil {
			return errors.New("a shared VM names no machine")
		}
	case "isolated":
		if b.Machine == nil || !ValidName(b.Machine.Name) || !ValidID(b.Machine.ID) {
			return errors.New("an isolated VM names its machine")
		}
	default:
		return errors.New("unknown VM role")
	}
	return nil
}

// Credential is the nsl.vm boot credential. The binding fields must match the
// VM's recorded binding on every boot after the first.
type Credential struct {
	Binding
	PublicKey   string  `json:"public_key"`
	Autostart   bool    `json:"autostart"`
	IdleTimeout int     `json:"idle_timeout"`
	Shares      []Share `json:"shares"`
	Aliases     []Alias `json:"aliases"`
}

// Share is a host tree mounted read-write at /mnt/host plus its canonical path.
type Share struct {
	Source string `json:"source"`
}

// Alias is a relative symlink under /mnt/host for a top-level host alias.
type Alias struct {
	Path   string `json:"path"`
	Target string `json:"target"`
}

var publicKeyPattern = regexp.MustCompile(`^ssh-ed25519 [A-Za-z0-9+/]+={0,2}( [^\r\n]*)?$`)

// Validate checks a credential completely.
func (c *Credential) Validate() error {
	if err := c.Binding.Validate(); err != nil {
		return err
	}
	if !publicKeyPattern.MatchString(c.PublicKey) || c.IdleTimeout < 0 || c.IdleTimeout > 1440 {
		return errors.New("invalid credential key or idle timeout")
	}
	if c.Role == "isolated" && (len(c.Shares) != 0 || len(c.Aliases) != 0) {
		return errors.New("an isolated VM has no host shares")
	}
	for _, s := range c.Shares {
		if !SafeHostPath(s.Source) {
			return fmt.Errorf("unsupported share %q", s.Source)
		}
	}
	for _, a := range c.Aliases {
		name := strings.TrimPrefix(a.Path, "/mnt/host/")
		if name == a.Path || name == "" || strings.Contains(name, "/") || !SafeHostPath("/"+name) ||
			a.Target == "" || path.IsAbs(a.Target) || !SafeHostPath("/"+a.Target) {
			return fmt.Errorf("unsupported alias %q", a.Path)
		}
	}
	return nil
}

// SafeHostPath reports whether p is a clean absolute path other than / that
// vmspawn and mount options can carry.
func SafeHostPath(p string) bool {
	return path.IsAbs(p) && p != "/" && path.Clean(p) == p && !strings.ContainsAny(p, ":,\x00\n\r\\")
}

// MachineRecord is the VM's record of one machine, on the state subvolume.
type MachineRecord struct {
	Schema  int     `json:"schema"`
	Name    string  `json:"name"`
	ID      string  `json:"id"`
	Account Account `json:"account"`
	BuildID string  `json:"build_id"`
	Created string  `json:"created"`
}

// MachineStatus is one entry of the machines operation.
type MachineStatus struct {
	Machine  string `json:"machine"`
	ID       string `json:"id"`
	State    string `json:"state"`
	Sessions int    `json:"sessions"`
	BuildID  string `json:"build_id"`
}

// CreateResult is the answer to create and import.
type CreateResult struct {
	BuildID string `json:"build_id"`
}

// ValidDigest reports whether s is sha256: followed by 64 lowercase hex digits.
func ValidDigest(s string) bool { return digestPattern.MatchString(s) }

// ValidBuildID reports whether s is an image build ID.
func ValidBuildID(s string) bool { return buildPattern.MatchString(s) }

// StartResult is the answer to start.
type StartResult struct {
	State   string  `json:"state"`
	Seconds float64 `json:"seconds"`
}

// ExitStatus maps systemd's ExecMainCode and ExecMainStatus to a shell-style
// status: the exit code, or 128+N for signal N.
func ExitStatus(code, status int32) int {
	const exited, killed, dumped = 1, 2, 3
	switch code {
	case exited:
		return int(status) & 0xff
	case killed, dumped:
		return 128 + int(status)
	}
	return ErrorExit
}
