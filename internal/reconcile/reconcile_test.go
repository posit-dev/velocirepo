package reconcile

import (
	"testing"

	"github.com/posit-dev/velocirepo/internal/source"
)

func TestDiffers(t *testing.T) {
	forks := source.ReferenceTotal{Value: 100, Tolerance: 5, MayExceed: true}
	issues := source.ReferenceTotal{Value: 100, Tolerance: 5}
	for _, c := range []struct {
		ref    source.ReferenceTotal
		stored int64
		want   bool
	}{
		{issues, 100, false}, {issues, 95, false}, {issues, 94, true}, {issues, 106, true},
		{forks, 150, false}, {forks, 94, true},
	} {
		if got := (Result{Reference: c.ref, Stored: c.stored}).Differs(); got != c.want {
			t.Errorf("%+v with %d stored: differs = %v, want %v", c.ref, c.stored, got, c.want)
		}
	}
	if (Result{Reference: issues, Stored: 0, Unavailable: "502"}).Differs() {
		t.Error("an unavailable reference differs")
	}
}
