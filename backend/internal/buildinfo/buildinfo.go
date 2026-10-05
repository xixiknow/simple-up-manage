// Package buildinfo carries the build-time version identity. The Dockerfile
// injects the release version via:
//
//	-ldflags "-X simple-up-manage/internal/buildinfo.Version=<version>"
//
// CI passes the semver of the v* git tag on the built commit when present
// (e.g. 1.2.3), falling back to the git short sha for untagged builds.
package buildinfo

var Version = "dev"
