package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
)

// supportCommand is `vesqira support-bundle` (ADR 0055): a client of the running owner API, like the agent commands. Without
// --out it only previews (file names and sizes, and with --show every file's text), so nothing is written before the person
// has seen what the bundle holds. Exit codes: 0 done, 1 the app did not answer or refused, 2 wrong usage.
func supportCommand(args []string) bool {
	if len(args) == 0 || args[0] != "support-bundle" {
		return false
	}
	os.Exit(runSupport(args[1:], os.Stdout, os.Stderr))
	return true
}

func runSupport(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("support-bundle", flag.ContinueOnError)
	fs.SetOutput(stderr)
	api := fs.String("api", defaultAPI, "the running app's loopback address")
	out := fs.String("out", "", "save the zip here (refuses to overwrite); without it only the preview is shown")
	names := fs.Bool("include-dataset-names", false, "include dataset names and file names (left out by default)")
	show := fs.Bool("show", false, "print the text of every file in the bundle")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: vesqira support-bundle [--include-dataset-names] [--show] [--out FILE.zip] [--api URL]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fs.Usage()
		return 2
	}
	c, err := newAPIClient(*api)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	var prev struct {
		ID    string `json:"id"`
		Bytes int    `json:"bytes"`
		Files []struct {
			Name  string `json:"name"`
			Bytes int    `json:"bytes"`
			Text  string `json:"text"`
		} `json:"files"`
	}
	if err := c.call("POST", "/api/support/preview", map[string]bool{"includeDatasetNames": *names}, &prev); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	for _, f := range prev.Files {
		fmt.Fprintf(stdout, "%-24s %7d bytes\n", f.Name, f.Bytes)
		if *show {
			fmt.Fprintf(stdout, "---\n%s---\n", f.Text)
		}
	}
	if *out == "" {
		fmt.Fprintln(stdout, "Nothing was saved. Add --out FILE.zip to save exactly these files.")
		return 0
	}
	resp, err := c.http.Get(c.base + "/api/support/bundle/" + prev.ID)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(stderr, "the app refused the download (%d)\n", resp.StatusCode)
		return 1
	}
	f, err := os.OpenFile(*out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	_, err = io.Copy(f, io.LimitReader(resp.Body, 64<<20))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(*out)
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "Saved %s\n", *out)
	return 0
}
