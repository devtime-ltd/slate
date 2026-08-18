package dockernet

import (
	"slices"
	"testing"
)

func TestCapacityFromPools(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want int
	}{
		{
			// Stock Docker Engine's defaults: 16 /16s out of the /12, plus
			// 16 /20s out of the /16.
			name: "stock docker engine",
			raw:  `[{"Base":"172.17.0.0/12","Size":16},{"Base":"192.168.0.0/16","Size":20}]`,
			want: 32,
		},
		{
			// OrbStack ships 30 individual /24s. Size equals the base prefix,
			// so each pool yields exactly one network - this is the shape that
			// makes the ceiling 30 rather than thousands.
			name: "orbstack single-subnet pools",
			raw:  `[{"Base":"192.168.97.0/24","Size":24},{"Base":"192.168.107.0/24","Size":24}]`,
			want: 2,
		},
		{
			name: "ipv6 pools are ignored",
			raw:  `[{"Base":"192.168.97.0/24","Size":24},{"Base":"fd07:b51a:cc66:d000::/56","Size":64}]`,
			want: 1,
		},
		{
			name: "a roomier pool yields many subnets",
			raw:  `[{"Base":"10.99.0.0/16","Size":27}]`,
			want: 2048,
		},
		{
			name: "malformed entries are skipped, not fatal",
			raw:  `[{"Base":"nonsense","Size":24},{"Base":"10.0.0.0/x","Size":24},{"Base":"10.1.0.0/24","Size":16},{"Base":"192.168.97.0/24","Size":24}]`,
			want: 1,
		},
		{
			name: "a pool split to the IPv4 limit",
			raw:  `[{"Base":"10.0.0.0/24","Size":32}]`,
			want: 256,
		},
		{
			name: "no pools",
			raw:  `[]`,
			want: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := capacityFromPools([]byte(tc.raw))
			if err != nil {
				t.Fatalf("capacityFromPools() error = %v", err)
			}
			if got != tc.want {
				t.Errorf("capacityFromPools() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestCapacityFromPoolsRejectsGarbage(t *testing.T) {
	if _, err := capacityFromPools([]byte("not json")); err == nil {
		t.Error("capacityFromPools() should error on non-JSON input")
	}
}

func TestParseIdle(t *testing.T) {
	// A stopped container detaches its endpoint, so a network belonging to a
	// merely-stopped workspace reports 0 and is reclaimable.
	out := "slate__sparta--alpha_default|0|slate__sparta--alpha\n" +
		"slate__sparta--beta_default|5|slate__sparta--beta\n" +
		"slate__hydra--gamma_default|0|slate__hydra--gamma\n" +
		"slate__objective--delta_default|1|slate__objective--delta\n"

	got := parseIdle(out, map[string]bool{})
	want := []string{"slate__sparta--alpha_default", "slate__hydra--gamma_default"}
	if !slices.Equal(got, want) {
		t.Errorf("parseIdle() = %v, want %v", got, want)
	}
}

func TestParseIdleSkipsBusyProjects(t *testing.T) {
	// A crash-looping container flickers to 0 endpoints between retries.
	// Reclaiming then would pull the network out from under a container that
	// is about to come back, so a restarting project is off limits.
	out := "slate__sparta--alpha_default|0|slate__sparta--alpha\n" +
		"slate__sparta--flapping_default|0|slate__sparta--flapping\n"

	got := parseIdle(out, map[string]bool{"slate__sparta--flapping": true})
	want := []string{"slate__sparta--alpha_default"}
	if !slices.Equal(got, want) {
		t.Errorf("parseIdle() = %v, want %v", got, want)
	}
}

func TestParseIdleSkipsNetworksComposeDidNotCreate(t *testing.T) {
	// A hand-made network matching the name prefix is not ours to delete, and
	// neither is a compose network from some other project.
	out := "slate__shared|0|\n" +
		"slate__handmade_default|0|\n" +
		"slate__other_default|0|unrelated-project\n" +
		"slate__sparta--real_default|0|slate__sparta--real\n"

	got := parseIdle(out, map[string]bool{})
	if !slices.Equal(got, []string{"slate__sparta--real_default"}) {
		t.Errorf("parseIdle() = %v, want [slate__sparta--real_default]", got)
	}
}

func TestParseIdleEmpty(t *testing.T) {
	if got := parseIdle("", map[string]bool{}); len(got) != 0 {
		t.Errorf("parseIdle(\"\") = %v, want empty", got)
	}
}
