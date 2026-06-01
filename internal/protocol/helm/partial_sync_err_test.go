package helm

// Typed partial-sync error.
//
// Table-driven coverage for the sentinel + constructor + Unwrap chain so
// callers can use errors.Is(err, ErrHelmPartialSync) AND errors.As(err, &pse)
// interchangeably. Test 5 (negative) locks the contract: only a
// PartialSyncErr satisfies the sentinel check — generic errors cannot wear
// the partial mask.

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestPartialSyncErr(t *testing.T) {
	t.Run("Is_and_As_direct", func(t *testing.T) {
		err := newPartialSyncErr(2, 3, nil)

		if !errors.Is(err, ErrHelmPartialSync) {
			t.Fatalf("errors.Is(err, ErrHelmPartialSync) = false; want true")
		}

		var pse *PartialSyncErr
		if !errors.As(err, &pse) {
			t.Fatalf("errors.As(err, &pse) = false; want true")
		}
		if got := pse.Persisted(); got != 2 {
			t.Errorf("Persisted() = %d; want 2", got)
		}
		if got := pse.Expected(); got != 3 {
			t.Errorf("Expected() = %d; want 3", got)
		}
	})

	t.Run("Is_and_As_through_outer_wrap", func(t *testing.T) {
		err := fmt.Errorf("outer: %w", newPartialSyncErr(1, 3, nil))

		if !errors.Is(err, ErrHelmPartialSync) {
			t.Fatalf("errors.Is(wrapped, ErrHelmPartialSync) = false; want true")
		}
		var pse *PartialSyncErr
		if !errors.As(err, &pse) {
			t.Fatalf("errors.As(wrapped, &pse) = false; want true")
		}
		if pse.Persisted() != 1 || pse.Expected() != 3 {
			t.Errorf("counts through wrap: persisted=%d expected=%d; want 1,3", pse.Persisted(), pse.Expected())
		}
	})

	t.Run("Cause_chain_reachable_and_in_error_string", func(t *testing.T) {
		cause := errors.New("upstream 500")
		err := newPartialSyncErr(2, 3, cause)

		if !errors.Is(err, ErrHelmPartialSync) {
			t.Errorf("errors.Is(err, ErrHelmPartialSync) = false; want true")
		}
		if !errors.Is(err, cause) {
			t.Errorf("errors.Is(err, cause) = false; want true (cause must be in the Unwrap chain)")
		}
		if !strings.Contains(err.Error(), "upstream 500") {
			t.Errorf("err.Error() = %q; want cause string 'upstream 500' present", err.Error())
		}
	})

	t.Run("Boundary_zero_persisted", func(t *testing.T) {
		err := newPartialSyncErr(0, 3, nil)
		var pse *PartialSyncErr
		if !errors.As(err, &pse) {
			t.Fatalf("errors.As = false; want true")
		}
		if pse.Persisted() != 0 {
			t.Errorf("Persisted() = %d; want 0", pse.Persisted())
		}
		if pse.Expected() != 3 {
			t.Errorf("Expected() = %d; want 3", pse.Expected())
		}
	})

	t.Run("Negative_different_error_does_not_match", func(t *testing.T) {
		other := errors.New("not partial")
		if errors.Is(other, ErrHelmPartialSync) {
			t.Errorf("errors.Is(other, ErrHelmPartialSync) = true; want false")
		}
		var pse *PartialSyncErr
		if errors.As(other, &pse) {
			t.Errorf("errors.As(other, &pse) = true; want false")
		}
	})
}
