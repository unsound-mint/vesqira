package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/unsound-mint/vesqira/services/api/internal/accounts"
	"github.com/unsound-mint/vesqira/services/api/internal/audit"
	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
)

// adminCommand is `vesqira-api admin ...`, the group server's root of trust (ADR 0050 section 2): whoever can run a command as
// the service account on the server machine. It works on the catalog file directly, beside or without a running server.
//
//	admin bootstrap --db PATH [--public-origin URL]       print a one-time setup link for a new admin (30 minutes, single use)
//	admin reset-second-factor --db PATH --username NAME   remove an account's TOTP and recovery codes and end its sessions
//
// Exit codes: 0 done, 1 failed, 2 wrong usage.
func adminCommand(args []string) bool {
	if len(args) == 0 || args[0] != "admin" {
		return false
	}
	os.Exit(runAdmin(args[1:], os.Stdout, os.Stderr))
	return true
}

func runAdmin(args []string, stdout, stderr io.Writer) int {
	usage := func() int {
		fmt.Fprintln(stderr, "usage: vesqira-api admin bootstrap --db PATH [--public-origin https://...]")
		fmt.Fprintln(stderr, "       vesqira-api admin reset-second-factor --db PATH --username NAME")
		return 2
	}
	if len(args) == 0 {
		return usage()
	}
	fs := flag.NewFlagSet("admin "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	db := fs.String("db", "", "the group server's catalog")
	origin := fs.String("public-origin", "", "the server's https origin, to print the whole link")
	username := fs.String("username", "", "the account (reset-second-factor)")
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 0 || *db == "" {
		return usage()
	}
	store, err := catalog.Open(*db)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer store.Close()
	acc, err := accounts.Open(store.DB())
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	log, err := audit.Open(store.DB())
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	cli := audit.Actor{ID: "system", Via: "cli"}
	switch args[0] {
	case "bootstrap":
		if *origin != "" {
			if u, err := url.Parse(*origin); err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
				fmt.Fprintln(stderr, "--public-origin must be an origin such as https://vesqira.example.edu")
				return 2
			}
		}
		if has, err := acc.HasAdmin(); err == nil && has {
			fmt.Fprintln(stderr, "note: this server already has an admin; the link creates another one")
		}
		secret, exp, err := acc.CreateSetupToken()
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		log.RecordAs(cli, "setup link issued", "account", 0, "expires "+exp.UTC().Format("2006-01-02 15:04 UTC"))
		// the secret travels in the fragment, so it never reaches a server log, a proxy log or a Referer header
		link := strings.TrimRight(*origin, "/") + "/#/setup/" + secret
		fmt.Fprintf(stdout, "Open this link within 30 minutes to create an admin account (it works once):\n\n  %s\n\n", link)
		if *origin == "" {
			fmt.Fprintln(stdout, "(prefix it with the server's https address)")
		}
		return 0
	case "reset-second-factor":
		a, err := acc.ByUsername(*username)
		if errors.Is(err, accounts.ErrNotFound) {
			fmt.Fprintf(stderr, "no account has the username %q\n", *username)
			return 1
		}
		var sessions, tokens int64
		if err == nil {
			sessions, tokens, err = acc.ResetSecondFactor(a.ID)
		}
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		log.RecordAs(cli, "second factor reset", "account", 0, "for "+a.ID+" ("+a.Username+")")
		log.RecordAs(cli, "sessions ended by the reset", "account", 0, fmt.Sprintf("%d sessions of %s", sessions, a.ID))
		log.RecordAs(cli, "access tokens revoked by the reset", "account", 0, fmt.Sprintf("%d tokens of %s", tokens, a.ID))
		fmt.Fprintf(stdout, "The second factor of %s is removed, %d sessions ended and %d access tokens revoked; they enrol again at their next sign-in.\n", a.Username, sessions, tokens)
		return 0
	}
	return usage()
}
