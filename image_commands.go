package main

import (
	"errors"
	"flag"
	"fmt"
	"strings"
)

func (a *app) imageCommand(args []string) error {
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fs.SetOutput(a.err)
	offline := fs.Bool("offline", false, "use a fresh signed catalogue and verified cache")
	selector := ""
	rest := args[1:]
	if args[0] == "pull" {
		if len(rest) == 0 {
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
	client := a.imageClient()
	if args[0] == "pull" {
		path, digest, err := client.pull(selector, *offline)
		if err != nil {
			return err
		}
		fmt.Fprintf(a.out, "Verified %s\n%s\n", digest, path)
		return nil
	}
	cat, err := client.catalogue(*offline)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.out, "Catalogue %d (expires %s)\n", cat.Sequence, cat.Expires.UTC().Format("2006-01-02T15:04:05Z"))
	for _, e := range cat.Images {
		fmt.Fprintf(a.out, "%s\t%s\t%s\t%s\n", strings.Join(e.Selectors, ", "), e.Architecture, e.BuildID, e.Manifest)
	}
	return nil
}
