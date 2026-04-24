package exedebug

import (
	"testing"

	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/tailcfg"
)

func TestIsHumanTailscaleUser(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		who  *apitype.WhoIsResponse
		want bool
	}{
		{
			name: "nil_response",
			who:  nil,
			want: false,
		},
		{
			name: "nil_node",
			who:  &apitype.WhoIsResponse{UserProfile: &tailcfg.UserProfile{LoginName: "alice@example.com"}},
			want: false,
		},
		{
			name: "tagged_node",
			who: &apitype.WhoIsResponse{
				Node:        &tailcfg.Node{Tags: []string{"tag:exelet"}},
				UserProfile: &tailcfg.UserProfile{LoginName: "alice@example.com"},
			},
			want: false,
		},
		{
			name: "tag_ops_is_still_tagged",
			who: &apitype.WhoIsResponse{
				Node:        &tailcfg.Node{Tags: []string{"tag:ops"}},
				UserProfile: &tailcfg.UserProfile{LoginName: "alice@example.com"},
			},
			want: false,
		},
		{
			name: "untagged_with_login",
			who: &apitype.WhoIsResponse{
				Node:        &tailcfg.Node{},
				UserProfile: &tailcfg.UserProfile{LoginName: "alice@example.com"},
			},
			want: true,
		},
		{
			name: "untagged_nil_profile",
			who: &apitype.WhoIsResponse{
				Node: &tailcfg.Node{},
			},
			want: false,
		},
		{
			name: "untagged_empty_login",
			who: &apitype.WhoIsResponse{
				Node:        &tailcfg.Node{},
				UserProfile: &tailcfg.UserProfile{LoginName: ""},
			},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsHumanTailscaleUser(tt.who); got != tt.want {
				t.Errorf("IsHumanTailscaleUser() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHasTailscaleTag(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		who  *apitype.WhoIsResponse
		tag  string
		want bool
	}{
		{
			name: "nil_response",
			who:  nil,
			tag:  "tag:ops",
			want: false,
		},
		{
			name: "nil_node",
			who:  &apitype.WhoIsResponse{},
			tag:  "tag:ops",
			want: false,
		},
		{
			name: "no_tags",
			who:  &apitype.WhoIsResponse{Node: &tailcfg.Node{}},
			tag:  "tag:ops",
			want: false,
		},
		{
			name: "tag_match",
			who:  &apitype.WhoIsResponse{Node: &tailcfg.Node{Tags: []string{"tag:ops"}}},
			tag:  "tag:ops",
			want: true,
		},
		{
			name: "tag_match_among_many",
			who:  &apitype.WhoIsResponse{Node: &tailcfg.Node{Tags: []string{"tag:exelet", "tag:ops", "tag:other"}}},
			tag:  "tag:ops",
			want: true,
		},
		{
			name: "tag_mismatch",
			who:  &apitype.WhoIsResponse{Node: &tailcfg.Node{Tags: []string{"tag:exelet"}}},
			tag:  "tag:ops",
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HasTailscaleTag(tt.who, tt.tag); got != tt.want {
				t.Errorf("HasTailscaleTag(_, %q) = %v, want %v", tt.tag, got, tt.want)
			}
		})
	}
}
