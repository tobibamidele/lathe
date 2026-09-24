package cli

import "runtime/debug"

// Version and Revision are set at build time by the Makefile (or the release
// workflow) with
//
//	-ldflags "-X github.com/tobibamidele/lathe/internal/cli.Version=v1.2.3 \
//	          -X github.com/tobibamidele/lathe/internal/cli.Revision=abc1234"
//
// On go install builds they stay default and [debug.ReadBuildInfo] supplies the
// module version (the tag, for "go install ...@v1.2.3") and, when the module
// was built from a git checkout, the VCS revision.
var (
	Version  = "dev"
	Revision = ""
)

// buildInfo narrows *debug.BuildInfo to the fields version output needs, so
// tests can hand versionLine a fake.
type buildInfo interface {
	MainVersion() string
	Setting(key string) string
}

type runtimeBuildInfo struct{ inner *debug.BuildInfo }

func (r runtimeBuildInfo) MainVersion() string { return r.inner.Main.Version }
func (r runtimeBuildInfo) Setting(key string) string {
	for _, s := range r.inner.Settings {
		if s.Key == key {
			return s.Value
		}
	}
	return ""
}

// versionLine renders "lathe <version> (<revision>)". The build-time ldflags
// win when set; otherwise the Go toolchain's embedded module version covers
// "go install module@version" and VCS builds. The git sha comes from either
// ldflags or the VCS build setting and is truncated to the short form.
func versionLine(version, revision string, bi buildInfo) string {
	if version == "" || version == "dev" {
		if bi != nil {
			if v := bi.MainVersion(); v != "" && v != "(devel)" {
				version = v
			}
		}
	}
	if revision == "" && bi != nil {
		revision = bi.Setting("vcs.revision")
	}
	if len(revision) >= 7 {
		revision = revision[:7]
	}
	if version == "" {
		version = "dev"
	}
	if revision != "" {
		return "lathe " + version + " (" + revision + ")"
	}
	return "lathe " + version
}
