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
	refresh := false
	if args[0] == "images" {
		fs.BoolVar(&refresh, "refresh", false, "refresh the signed catalogue")
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
	fmt.Fprintf(a.out, "Catalogue %d (expires %s)\n", cat.Sequence, cat.Expires.UTC().Format("2006-01-02T15:04:05Z"))
	w := tabwriter.NewWriter(a.out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "KIND\tSELECTORS\tBUILD\tCACHED")
	for _, e := range cat.Images {
		if e.Architecture != "x86-64" {
			continue
		}
		cached := "no"
		if _, err := os.Lstat(filepath.Join(base, strings.TrimPrefix(e.Manifest, "sha256:"), "receipt.json")); err == nil {
			cached = "yes"
		}
		selectors := strings.Join(e.Selectors, ", ")
		if e.Kind == "vm" {
			selectors = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", e.Kind, selectors, e.BuildID, cached)
	}
	if err = w.Flush(); err != nil {
		return err
	}
	v, err := a.loadVM()
	if err != nil {
		return err
	}
	switch {
	case v == nil || (v.Image == "" && v.PendingImage == ""):
		fmt.Fprintln(a.out, "VM image: none selected yet")
	case v.PendingImage != "":
		fmt.Fprintf(a.out, "VM image: sha256:%s at the next VM start\n", v.PendingImage[:12])
	default:
		build := v.ImageBuild
		if build == "" {
			build = "sha256:" + v.Image[:12]
		}
		fmt.Fprintln(a.out, "VM image in effect:", build)
	}
	return nil
}
