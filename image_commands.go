package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
)

func (a *app) imageCommand(args []string) error {
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fs.SetOutput(a.err)
	offline := fs.Bool("offline", false, "use a fresh signed catalogue and verified cache")
	refresh, asJSON := false, false
	if args[0] == "images" {
		fs.BoolVar(&refresh, "refresh", false, "refresh the signed catalogue")
		fs.BoolVar(&asJSON, "json", false, "print JSON for other programs")
	}
	selector := ""
	rest := args[1:]
	if args[0] == "pull" {
		if len(rest) == 0 || strings.HasPrefix(rest[0], "-") {
			return errors.New("usage: pull DISTRO:RELEASE [--offline]")
		}
		selector = rest[0]
		rest = rest[1:]
	}
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected image command arguments")
	}
	if *offline && refresh {
		return errors.New("--offline and --refresh are mutually exclusive")
	}
	client := a.imageClient()
	if args[0] == "pull" {
		image, err := client.pullMachine(selector, *offline)
		if err != nil {
			return err
		}
		fmt.Fprintf(a.out, "Verified %s\n%s\n", image.buildID, image.path)
		return nil
	}
	mode := catalogueCached
	if *offline {
		mode = catalogueOffline
	} else if refresh {
		mode = catalogueRefresh
	}
	cat, err := client.catalogue(mode)
	if err != nil {
		return err
	}
	base, err := client.init()
	if err != nil {
		return err
	}
	images := []imageListing{}
	for _, e := range cat.Images {
		if e.Architecture != "x86-64" {
			continue
		}
		_, err := os.Lstat(filepath.Join(base, strings.TrimPrefix(e.Manifest, "sha256:"), "receipt.json"))
		l := imageListing{Kind: e.Kind, Selectors: []string{}, Build: e.BuildID, Manifest: e.Manifest, Cached: err == nil}
		if e.Kind != "vm" {
			l.Selectors = append(l.Selectors, e.Selectors...)
		}
		images = append(images, l)
	}
	v, err := a.loadVM()
	if err != nil {
		return err
	}
	var selected *vmImageListing
	switch {
	case v == nil || (v.Image == "" && v.PendingImage == ""):
	case v.PendingImage != "":
		selected = &vmImageListing{Image: "sha256:" + v.PendingImage, Pending: true}
	default:
		selected = &vmImageListing{Image: imageName(v.ImageBuild, v.Image)}
	}
	expires := cat.Expires.UTC().Format("2006-01-02T15:04:05Z")
	if asJSON {
		type catalogueListing struct {
			Sequence int64  `json:"sequence"`
			Expires  string `json:"expires"`
		}
		return writeJSON(a.out, struct {
			Catalogue catalogueListing `json:"catalogue"`
			Images    []imageListing   `json:"images"`
			VMImage   *vmImageListing  `json:"vm_image"`
		}{catalogueListing{cat.Sequence, expires}, images, selected})
	}
	fmt.Fprintf(a.out, "Catalogue %d (expires %s)\n", cat.Sequence, expires)
	w := tabwriter.NewWriter(a.out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "KIND\tSELECTORS\tBUILD\tCACHED")
	for _, l := range images {
		cached, selectors := "no", strings.Join(l.Selectors, ", ")
		if l.Cached {
			cached = "yes"
		}
		if l.Kind == "vm" {
			selectors = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", l.Kind, selectors, l.Build, cached)
	}
	if err = w.Flush(); err != nil {
		return err
	}
	switch {
	case selected == nil:
		fmt.Fprintln(a.out, "VM image: none selected yet")
	case selected.Pending:
		fmt.Fprintf(a.out, "VM image: %s at the next VM start\n", shortImage(selected.Image))
	default:
		fmt.Fprintln(a.out, "VM image in effect:", shortImage(selected.Image))
	}
	return nil
}

// imageListing is one catalogue entry in images, as the table and as JSON.
type imageListing struct {
	Kind      string   `json:"kind"`
	Selectors []string `json:"selectors"`
	Build     string   `json:"build"`
	Manifest  string   `json:"manifest"`
	Cached    bool     `json:"cached"`
}

// vmImageListing is the VM image selected for nsl's VMs.
type vmImageListing struct {
	Image   string `json:"image"`
	Pending bool   `json:"pending"`
}
