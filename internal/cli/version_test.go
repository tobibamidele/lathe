package cli

import "testing"

type fakeBuildInfo struct {
	version  string
	settings map[string]string
}

func (f fakeBuildInfo) MainVersion() string       { return f.version }
func (f fakeBuildInfo) Setting(key string) string { return f.settings[key] }

func TestVersionLine(t *testing.T) {
	rel := fakeBuildInfo{version: "(devel)", settings: map[string]string{"vcs.revision": ""}}
	installed := fakeBuildInfo{version: "v0.3.0", settings: map[string]string{"vcs.revision": ""}}
	local := fakeBuildInfo{version: "(devel)", settings: map[string]string{"vcs.revision": "0x1a2b3c4d5e6f70"}}
	none := fakeBuildInfo{version: "(devel)", settings: map[string]string{}}

	cases := []struct {
		name     string
		version  string
		revision string
		bi       buildInfo
		want     string
	}{
		{"release ldflags", "v0.3.0", "abcdef1", rel, "lathe v0.3.0 (abcdef1)"},
		{"ldflags beat build info", "v0.3.0", "abcdef1", fakeBuildInfo{version: "v1.5.0", settings: map[string]string{"vcs.revision": "ffff"}}, "lathe v0.3.0 (abcdef1)"},
		{"go install @version", "dev", "", installed, "lathe v0.3.0"},
		{"go install @version with sha", "dev", "", fakeBuildInfo{version: "v0.3.0", settings: map[string]string{"vcs.revision": "0123456789abcdef"}}, "lathe v0.3.0 (0123456)"},
		{"local build", "dev", "", local, "lathe dev (0x1a2b3)"},
		{"no info", "dev", "", none, "lathe dev"},
		{"short sha truncated from ldflags", "v0.9.0", "0123456789abcdef", none, "lathe v0.9.0 (0123456)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := versionLine(tc.version, tc.revision, tc.bi); got != tc.want {
				t.Errorf("versionLine() = %q, want %q", got, tc.want)
			}
		})
	}
}
