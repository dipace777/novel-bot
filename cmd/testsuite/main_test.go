package main

import (
	"io"
	"strings"
	"testing"
)

func TestRequiredEvidenceCannotSkipOrDisappear(t *testing.T) {
	required := map[string]bool{"novel-bot/example::TestRequired": true}
	for _, tc := range []struct {
		name, events string
		ok           bool
	}{
		{"pass", `{"Action":"pass","Package":"novel-bot/example","Test":"TestRequired"}`, true},
		{"skip", `{"Action":"skip","Package":"novel-bot/example","Test":"TestRequired"}`, false},
		{"missing", `{"Action":"pass","Package":"novel-bot/example"}`, false},
		{"malformed", `broken json`, false},
		{"subtest_skip", `{"Action":"skip","Package":"novel-bot/example","Test":"TestRequired/child"}` + "\n" + `{"Action":"pass","Package":"novel-bot/example","Test":"TestRequired"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := collect(strings.NewReader(tc.events), io.Discard, io.Discard, required, func(s string) string { return s })
			if (err == nil) != tc.ok {
				t.Fatalf("evidence acceptance: %v", err)
			}
		})
	}
}
