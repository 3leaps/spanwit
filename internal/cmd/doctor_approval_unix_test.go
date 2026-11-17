//go:build darwin || linux

package cmd

import (
	"errors"
	"strings"
	"testing"
)

func TestApprovalSigningReport(t *testing.T) {
	ok := func(string) error { return nil }
	cases := []struct {
		goos   string
		brew   bool
		expect string
	}{
		{"darwin", true, "sign: approval sign --ssh-keygen /opt/homebrew/bin/ssh-keygen"},
		{"darwin", false, "brew install openssh"},
		{"linux", false, "sign: /usr/bin/ssh-keygen"},
	}
	for _, c := range cases {
		lines := approvalSigningReport(c.goos, func(string) bool { return c.brew }, ok)
		if got := lines[len(lines)-1]; !strings.Contains(got, c.expect) {
			t.Errorf("%s brew=%v: %q", c.goos, c.brew, got)
		}
	}
	lines := approvalSigningReport("linux", func(string) bool { return false }, func(string) error { return errors.New("owned by the executing user") })
	if !strings.Contains(lines[0], "untrusted") || !strings.Contains(lines[1], "not usable") {
		t.Errorf("untrusted files not reported: %q", lines)
	}
}
