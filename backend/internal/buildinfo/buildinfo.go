// Package buildinfo carries the build-time version identity. The Dockerfile
// injects the git short sha via:
//
//	-ldflags "-X simple-up-manage/internal/buildinfo.Version=<sha>"
package buildinfo

var Version = "dev"
