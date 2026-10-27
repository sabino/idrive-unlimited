package probe

import (
	"context"
	"errors"
	"testing"

	"idrive-unlimited/internal/evs"
	"idrive-unlimited/internal/testevs"
)

func TestProbeVerifiesCopiesAndCleansUpFailures(t *testing.T) {
	for _, tc := range []struct {
		name    string
		options testevs.Options
		success bool
	}{
		{"success", testevs.Options{}, true},
		{"failed upload", testevs.Options{FailUpload: true}, false},
		{"corrupted copy", testevs.Options{CorruptCopy: true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := testevs.New(t)
			s.Configure(tc.options)
			c, _ := evs.NewClient(s.Host, nil)
			r, err := Run(context.Background(), c, "/", "/.probe")
			if r.Success != tc.success || (err == nil) != tc.success {
				t.Fatalf("success=%v err=%v", r.Success, err)
			}
			if _, err := c.StatFresh(context.Background(), r.ScratchPath); !errors.Is(err, evs.ErrNotFound) {
				t.Fatalf("scratch not cleaned: %v", err)
			}
			if !tc.success && tc.name == "corrupted copy" {
				last := ""
				for _, step := range r.Steps {
					if !step.OK {
						last = step.Name
					}
				}
				if last != "verify-copied-small" {
					t.Fatalf("failed step=%s", last)
				}
			}
		})
	}
}
