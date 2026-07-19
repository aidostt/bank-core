package domain

import "testing"

// The exhaustive test the transfer doc requires: every (state, event) pair
// is asserted — legal pairs against the expected target, everything else
// must return ErrIllegalTransition.
func TestNextExhaustive(t *testing.T) {
	type key struct {
		s State
		e Event
	}
	legal := map[key]State{
		{StateCreated, EvStart}:                StateValidating,
		{StateValidating, EvValidated}:         StateHeld,
		{StateValidating, EvValidationFailed}:  StateFailed,
		{StateHeld, EvPostingStarted}:          StatePosting,
		{StateHeld, EvHoldFailed}:              StateReleasing,
		{StateHeld, EvRecoveryExhausted}:       StateReleasing,
		{StatePosting, EvPosted}:               StateCompleted,
		{StatePosting, EvPostFailed}:           StateReleasing,
		{StatePosting, EvRecoveryExhausted}:    StateReleasing,
		{StateReleasing, EvReleased}:           StateFailed,
	}

	checked := 0
	for _, s := range AllStates {
		for _, e := range AllEvents {
			got, err := Next(s, e)
			want, ok := legal[key{s, e}]
			if ok {
				if err != nil || got != want {
					t.Errorf("Next(%s, %s) = (%s, %v), want (%s, nil)", s, e, got, err, want)
				}
			} else if err != ErrIllegalTransition {
				t.Errorf("Next(%s, %s) = (%s, %v), want ErrIllegalTransition", s, e, got, err)
			}
			checked++
		}
	}
	if want := len(AllStates) * len(AllEvents); checked != want {
		t.Fatalf("checked %d pairs, want %d", checked, want)
	}
	// Terminal states accept no events at all.
	for _, s := range []State{StateCompleted, StateFailed} {
		if !IsTerminal(s) {
			t.Errorf("%s must be terminal", s)
		}
		if len(transitions[s]) != 0 {
			t.Errorf("terminal state %s has outgoing transitions", s)
		}
	}
	// Every non-terminal state has a path to a terminal state (no traps).
	for _, s := range AllStates {
		if IsTerminal(s) {
			continue
		}
		if !reachesTerminal(s, map[State]bool{}) {
			t.Errorf("state %s cannot reach a terminal state", s)
		}
	}
}

func reachesTerminal(s State, seen map[State]bool) bool {
	if IsTerminal(s) {
		return true
	}
	if seen[s] {
		return false
	}
	seen[s] = true
	for _, next := range transitions[s] {
		if reachesTerminal(next, seen) {
			return true
		}
	}
	return false
}

func TestValidateSpec(t *testing.T) {
	cases := []struct {
		name    string
		typ     Type
		from    string
		to      string
		number  string
		amount  int64
		cur     string
		wantErr error
	}{
		{"topup ok", TypeTopup, "", "acc-1", "", 100, "KZT", nil},
		{"topup with from", TypeTopup, "acc-0", "acc-1", "", 100, "KZT", ErrInvalidSpec},
		{"topup without to", TypeTopup, "", "", "", 100, "KZT", ErrInvalidSpec},
		{"internal ok", TypeInternal, "acc-1", "acc-2", "", 100, "USD", nil},
		{"internal same account", TypeInternal, "acc-1", "acc-1", "", 100, "KZT", ErrSameAccount},
		{"internal missing to", TypeInternal, "acc-1", "", "", 100, "KZT", ErrInvalidSpec},
		{"p2p ok", TypeP2P, "acc-1", "", "KZ86125KZT00000001", 100, "KZT", nil},
		{"p2p missing number", TypeP2P, "acc-1", "", "", 100, "KZT", ErrInvalidSpec},
		{"zero amount", TypeP2P, "acc-1", "", "n", 0, "KZT", ErrInvalidSpec},
		{"negative amount", TypeInternal, "a", "b", "", -5, "KZT", ErrInvalidSpec},
		{"bad currency", TypeInternal, "a", "b", "", 5, "EUR", ErrInvalidSpec},
		{"unknown type", Type("CHEQUE"), "a", "b", "", 5, "KZT", ErrInvalidSpec},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := ValidateSpec(c.typ, c.from, c.to, c.number, c.amount, c.cur); err != c.wantErr {
				t.Fatalf("got %v, want %v", err, c.wantErr)
			}
		})
	}
}

func TestCheckLimits(t *testing.T) {
	if err := CheckLimits(100, 1000, 0, 5000); err != nil {
		t.Fatal(err)
	}
	if err := CheckLimits(1001, 1000, 0, 5000); err != ErrLimitExceeded {
		t.Fatalf("per-tx: %v", err)
	}
	if err := CheckLimits(100, 1000, 4901, 5000); err != ErrLimitExceeded {
		t.Fatalf("daily: %v", err)
	}
	// boundary: exactly hitting the daily limit is allowed
	if err := CheckLimits(100, 1000, 4900, 5000); err != nil {
		t.Fatalf("boundary: %v", err)
	}
	// overflow-safe: dailyUsed + amount would overflow int64
	if err := CheckLimits(1<<62, 1<<63-1, 1<<62, 1<<63-1); err != ErrLimitExceeded {
		t.Fatalf("overflow: %v", err)
	}
}
