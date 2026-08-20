package main

import (
	"testing"

	"github.com/seorilabs/platform/server/internal/config"
)

func TestRoleUsesIdentityIncludesSessionConsumers(t *testing.T) {
	tests := []struct {
		role config.Role
		want bool
	}{
		{role: config.RoleAPI, want: true},
		{role: config.RoleIAP, want: true},
		{role: config.RoleIngest, want: true},
		{role: config.RoleAds, want: true},
		{role: config.RoleAdmin, want: false},
		{role: config.RoleWorker, want: false},
	}

	for _, tt := range tests {
		t.Run(string(tt.role), func(t *testing.T) {
			if got := roleUsesIdentity(tt.role); got != tt.want {
				t.Fatalf("roleUsesIdentity(%q) = %t, want %t", tt.role, got, tt.want)
			}
		})
	}
}
