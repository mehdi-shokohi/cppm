package main

import "testing"

// The case that shipped wrong in v0.3.1: install.sh built from a clean checkout
// sitting on the tag, Go recorded the module version as v0.3.1, and cppm
// reported the compiled-in "0.3.1-dev" anyway -- so a real release install
// called itself a development build, and wrote that into users' cppm.lock.
func TestChooseVersion(t *testing.T) {
	cases := []struct {
		name   string
		stamp  string
		module string
		want   string
	}{
		{"release build", "v0.3.2", "v0.3.2", "v0.3.2"},
		{"the ldflags stamp wins", "v0.3.2", "(devel)", "v0.3.2"},
		{"go install module@tag", "", "v0.3.1", "v0.3.1"},
		{"source build on a clean tag", "", "v0.3.1", "v0.3.1"},
		{"a commit between tags names the commit", "", "v0.3.2-0.20261108113847-f4e8aac07f02", "v0.3.2-0.20261108113847-f4e8aac07f02"},
		// Nothing useful to report: the caller keeps its own fallback, which at
		// least carries a number.
		{"unnamed build falls back", "", "(devel)", ""},
		{"no build info at all", "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := chooseVersion(c.stamp, c.module); got != c.want {
				t.Errorf("chooseVersion(%q, %q) = %q, want %q", c.stamp, c.module, got, c.want)
			}
		})
	}
}
