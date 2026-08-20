//go:build linux

package main

// Container paths, because on Linux springback ships as an image: these are exactly where the
// compose files mount the volumes, so `springback serve` with no flags inside the image is
// already configured correctly.
const (
	defaultLibrary  = "/library"
	defaultAccounts = "/accounts"
	defaultLockdown = "/var/lib/lockdown"
)
