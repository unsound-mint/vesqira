//go:build !linux

package main

// networkFS cannot tell on this system; the group server is documented for Linux.
func networkFS(string) (bool, string) { return false, "" }
