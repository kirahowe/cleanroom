package main

import "testing"

func TestLocationFlags(t *testing.T) {
	for _, tc := range []struct{ policy, state, want string }{
		{"", "", ""},
		{"/etc/policy.jsonc", "", " --policy /etc/policy.jsonc"},
		{"", "/var/state", " --state /var/state"},
		{"/home/ann/My Policies/p.jsonc", "/tmp/it's", ` --policy '/home/ann/My Policies/p.jsonc' --state '/tmp/it'\''s'`},
	} {
		if got := locationFlags(tc.policy, tc.state); got != tc.want {
			t.Errorf("locationFlags(%q, %q) = %q, want %q", tc.policy, tc.state, got, tc.want)
		}
	}
}
