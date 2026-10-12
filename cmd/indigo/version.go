package main

import "runtime/debug"

// version is stamped into release builds with
// -ldflags "-X main.version=v1.2.3" (see the Makefile's VERSION).
var version = ""

// versionString reports what `indigo --version` prints. A release build
// answers with its stamped tag; anything else falls back to what the Go
// toolchain recorded, which for a build inside a checkout is a pseudo-version
// naming the commit (and "+dirty" for uncommitted changes).
func versionString() string {
	if version != "" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return "dev"
}
