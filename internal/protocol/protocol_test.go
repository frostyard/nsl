package protocol

import (
	"encoding/base64"
	"errors"
	"reflect"
	"strings"
	"testing"
)

const testID = "4f0c6a1e9b2d4c7f8e3a5b6c7d8e9f01"

func minutes(n int) *int { return &n }

func runRequest() Request {
	return Request{Protocol: Version, Op: "run", Machine: "debian", ID: testID,
		Argv: []string{"printf", "%s\\n", "$HOME", "with space", ""}, Directory: "/mnt/host/var/home/u",
		Env: map[string]string{"TERM": "xterm-256color", "LC_ALL": "C.UTF-8"}, IdleTimeout: minutes(15)}
}

func code(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func TestRoundTrip(t *testing.T) {
	for _, r := range []Request{
		runRequest(),
		{Protocol: Version, Op: "identity"},
		{Protocol: Version, Op: "vm", Argv: []string{"findmnt", "/var/lib/machines"}},
		{Protocol: Version, Op: "start", Machine: "a", ID: testID, IdleTimeout: minutes(0)},
		{Protocol: Version, Op: "create", Machine: "fedora-44", ID: testID, TimeZone: "America/New_York",
			Account: &Account{User: "bjk", Group: "bjk", UID: 1000, GID: 1000},
			Image:   &Image{Path: ImageShare + "/" + strings.Repeat("a", 64) + ".tar.zst", Digest: "sha256:" + strings.Repeat("a", 64), Size: 7, BuildID: "nsl-machine-debian-13-x86-64-r1"}},
	} {
		s, err := Encode(r)
		if err != nil {
			t.Fatalf("%s: %v", r.Op, err)
		}
		got, err := Decode(s)
		if err != nil || !reflect.DeepEqual(*got, r) {
			t.Fatalf("%s: %v %+v", r.Op, err, got)
		}
	}
}

func TestDecodeRefusesAmbiguousFraming(t *testing.T) {
	for _, tc := range []struct{ name, json, code string }{
		{"unknown field", `{"protocol":1,"op":"identity","extra":1}`, CodeBadRequest},
		{"duplicate key", `{"protocol":1,"op":"identity","op":"identity"}`, CodeBadRequest},
		{"trailing data", `{"protocol":1,"op":"identity"} {}`, CodeBadRequest},
		{"not an object", `[1]`, CodeBadRequest},
		{"wrong protocol", `{"protocol":2,"op":"identity"}`, CodeProtocol},
		{"unknown op", `{"protocol":1,"op":"shell"}`, CodeBadRequest},
	} {
		_, err := Decode(base64.StdEncoding.EncodeToString([]byte(tc.json)))
		if code(err) != tc.code {
			t.Fatalf("%s: %v", tc.name, err)
		}
	}
	for _, s := range []string{"not base64!", base64.RawStdEncoding.EncodeToString([]byte(`{"protocol": 1,"op":"identity"}`)), strings.Repeat("A", 100000)} {
		if _, err := Decode(s); code(err) != CodeBadRequest {
			t.Fatalf("%q: %v", s[:10], err)
		}
	}
}

func TestValidateRefusesBadFields(t *testing.T) {
	for name, change := range map[string]func(*Request){
		"relative directory":   func(r *Request) { r.Directory = "work" },
		"PATH in env":          func(r *Request) { r.Env["PATH"] = "/tmp" },
		"newline in env":       func(r *Request) { r.Env["LANG"] = "C\nX=1" },
		"lowercase lc":         func(r *Request) { r.Env["LC_all"] = "C" },
		"long env value":       func(r *Request) { r.Env["TERM"] = strings.Repeat("x", 257) },
		"NUL in argv":          func(r *Request) { r.Argv = []string{"a\x00b"} },
		"empty argv":           func(r *Request) { r.Argv = []string{} },
		"missing argv":         func(r *Request) { r.Argv = nil },
		"missing idle timeout": func(r *Request) { r.IdleTimeout = nil },
		"idle timeout range":   func(r *Request) { r.IdleTimeout = minutes(1441) },
		"negative idle":        func(r *Request) { r.IdleTimeout = minutes(-1) },
		"bad name":             func(r *Request) { r.Machine = "Debian" },
		"trailing hyphen":      func(r *Request) { r.Machine = "debian-" },
		"traversal name":       func(r *Request) { r.Machine = "../x" },
		"bad id":               func(r *Request) { r.ID = strings.ToUpper(testID) },
		"missing id":           func(r *Request) { r.ID = "" },
		"foreign field":        func(r *Request) { r.TimeZone = "UTC" },
		"foreign account":      func(r *Request) { r.Account = &Account{"u", "u", 1000, 1000} },
	} {
		r := runRequest()
		change(&r)
		if _, err := Encode(r); code(err) != CodeBadRequest {
			t.Fatalf("%s: %v", name, err)
		}
	}
	image := &Image{Path: ImageShare + "/" + strings.Repeat("b", 64) + ".tar.zst", Digest: "sha256:" + strings.Repeat("b", 64), Size: 1, BuildID: "b"}
	for name, r := range map[string]Request{
		"root account":     {Account: &Account{"root", "root", 0, 0}, Image: image, TimeZone: "UTC"},
		"nobody":           {Account: &Account{"u", "u", 65534, 1000}, Image: image, TimeZone: "UTC"},
		"bad user":         {Account: &Account{"Bad", "u", 1000, 1000}, Image: image, TimeZone: "UTC"},
		"image outside":    {Account: &Account{"u", "u", 1000, 1000}, Image: &Image{"/etc/" + strings.Repeat("b", 64) + ".tar.zst", image.Digest, 1, "b"}, TimeZone: "UTC"},
		"digest mismatch":  {Account: &Account{"u", "u", 1000, 1000}, Image: &Image{image.Path, "sha256:" + strings.Repeat("c", 64), 1, "b"}, TimeZone: "UTC"},
		"empty image":      {Account: &Account{"u", "u", 1000, 1000}, Image: &Image{image.Path, image.Digest, 0, "b"}, TimeZone: "UTC"},
		"zone traversal":   {Account: &Account{"u", "u", 1000, 1000}, Image: image, TimeZone: "../../etc/passwd"},
		"missing zone":     {Account: &Account{"u", "u", 1000, 1000}, Image: image},
		"missing image":    {Account: &Account{"u", "u", 1000, 1000}, TimeZone: "UTC"},
		"argv with create": {Account: &Account{"u", "u", 1000, 1000}, Image: image, TimeZone: "UTC", Argv: []string{"x"}},
	} {
		r.Protocol, r.Op, r.Machine, r.ID = Version, "create", "m", testID
		if _, err := Encode(r); code(err) != CodeBadRequest {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestExitStatus(t *testing.T) {
	for _, tc := range []struct {
		code, status int32
		want         int
	}{{1, 0, 0}, {1, 42, 42}, {1, 203, 203}, {2, 15, 143}, {2, 2, 130}, {2, 1, 129}, {2, 13, 141}, {2, 9, 137}, {3, 11, 139}, {0, 0, 255}} {
		if got := ExitStatus(tc.code, tc.status); got != tc.want {
			t.Fatalf("%d/%d: %d", tc.code, tc.status, got)
		}
	}
}

func TestCredential(t *testing.T) {
	good := func() Credential {
		return Credential{Binding: Binding{Version: 1, ID: testID, Role: "shared", UID: 1000, GID: 1000},
			PublicKey: "ssh-ed25519 AAAA nsl-vm", Autostart: true, IdleTimeout: 15,
			Shares: []Share{{"/var/home/u"}, {"/mnt"}}, Aliases: []Alias{{"/mnt/host/home", "var/home"}}}
	}
	c := good()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*Credential){
		"root uid":        func(c *Credential) { c.UID = 0 },
		"bad role":        func(c *Credential) { c.Role = "trusted" },
		"shared machine":  func(c *Credential) { c.Machine = &MachineRef{"m", testID} },
		"isolated shares": func(c *Credential) { c.Role, c.Machine = "isolated", &MachineRef{"m", testID} },
		"relative share":  func(c *Credential) { c.Shares = []Share{{"home/u"}} },
		"root share":      func(c *Credential) { c.Shares = []Share{{"/"}} },
		"colon share":     func(c *Credential) { c.Shares = []Share{{"/a:b"}} },
		"unclean share":   func(c *Credential) { c.Shares = []Share{{"/a/../b"}} },
		"nested alias":    func(c *Credential) { c.Aliases = []Alias{{"/mnt/host/a/b", "c"}} },
		"absolute target": func(c *Credential) { c.Aliases = []Alias{{"/mnt/host/home", "/var/home"}} },
		"escaping target": func(c *Credential) { c.Aliases = []Alias{{"/mnt/host/home", "../etc"}} },
		"outside alias":   func(c *Credential) { c.Aliases = []Alias{{"/etc/home", "var/home"}} },
		"two keys":        func(c *Credential) { c.PublicKey = "ssh-ed25519 AAAA\nssh-ed25519 BBBB" },
		"rsa key":         func(c *Credential) { c.PublicKey = "ssh-rsa AAAA" },
	} {
		c := good()
		change(&c)
		if c.Validate() == nil {
			t.Fatal(name)
		}
	}
	isolated := good()
	isolated.Role, isolated.Machine, isolated.Shares, isolated.Aliases = "isolated", &MachineRef{"m", testID}, nil, nil
	if err := isolated.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestParseError(t *testing.T) {
	e := ParseError("warning\nnsl-agent: machine-id: debian was replaced\n")
	if e == nil || e.Code != CodeMachineID || e.Message != "debian was replaced" {
		t.Fatal(e)
	}
	if ParseError("plain failure") != nil {
		t.Fatal("parsed a non-agent line")
	}
}
