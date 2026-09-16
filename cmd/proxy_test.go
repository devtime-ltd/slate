package cmd

import (
	"fmt"
	"slices"
	"testing"
)

// The port-conflict check must ask lsof for TCP listeners only. `-i :443`
// selects UDP too, and `-sTCP:LISTEN` constrains state for TCP alone, so UDP
// rows survive the filter: every outbound HTTP/3 (QUIC) socket to a remote
// :443 then looks like a local listener and `slate proxy start` refuses to
// start, naming whichever app happens to be first in lsof's output.
func TestPortConflictArgs(t *testing.T) {
	cases := []struct {
		name string
		port int
		want string
	}{
		{"http", 80, "-iTCP:80"},
		{"https", 443, "-iTCP:443"},
		{"custom https", 8443, "-iTCP:8443"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := portConflictArgs(tc.port)

			if !slices.Contains(args, tc.want) {
				t.Errorf("portConflictArgs(%d) = %v, want it to contain %q", tc.port, args, tc.want)
			}
			// The protocol-agnostic spellings are the bug, in either form.
			for _, bad := range []string{"-i", fmt.Sprintf(":%d", tc.port), fmt.Sprintf("-i:%d", tc.port)} {
				if slices.Contains(args, bad) {
					t.Errorf("portConflictArgs(%d) = %v, must not contain the protocol-agnostic %q", tc.port, args, bad)
				}
			}
			if !slices.Contains(args, "-sTCP:LISTEN") {
				t.Errorf("portConflictArgs(%d) = %v, want it to contain %q", tc.port, args, "-sTCP:LISTEN")
			}
		})
	}
}
