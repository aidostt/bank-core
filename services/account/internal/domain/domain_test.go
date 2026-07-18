package domain

import "testing"

func TestGenerateNumberCheckDigits(t *testing.T) {
	n, err := GenerateNumber("1234567890123")
	if err != nil {
		t.Fatal(err)
	}
	if len(n) != 20 || n[:2] != "KZ" {
		t.Fatalf("bad shape: %s", n)
	}
	if !ValidNumber(n) {
		t.Fatalf("check digits invalid: %s", n)
	}
}

func TestValidNumberRejectsCorruption(t *testing.T) {
	n, _ := GenerateNumber("0000000000042")
	// flip one digit
	corrupted := n[:19] + string('0'+(n[19]-'0'+1)%10)
	if ValidNumber(corrupted) {
		t.Fatalf("corrupted number accepted: %s", corrupted)
	}
	if ValidNumber("KZ001250000000000000X") {
		t.Fatal("non-digit accepted")
	}
	if ValidNumber("DE00123") {
		t.Fatal("wrong country/length accepted")
	}
}

func TestGenerateNumberRejectsBadRandom(t *testing.T) {
	if _, err := GenerateNumber("123"); err != ErrBadRandomPart {
		t.Fatal("short random part accepted")
	}
	if _, err := GenerateNumber("12345678901ab"); err != ErrBadRandomPart {
		t.Fatal("non-digit random part accepted")
	}
}

func TestStatusTransitions(t *testing.T) {
	cases := []struct {
		from, to string
		ok       bool
	}{
		{StatusActive, StatusFrozen, true},
		{StatusActive, StatusClosed, true},
		{StatusFrozen, StatusActive, true},
		{StatusFrozen, StatusClosed, true},
		{StatusClosed, StatusActive, false},
		{StatusClosed, StatusFrozen, false},
		{StatusActive, StatusActive, false},
	}
	for _, c := range cases {
		if got := CanTransition(c.from, c.to); got != c.ok {
			t.Errorf("CanTransition(%s→%s) = %v, want %v", c.from, c.to, got, c.ok)
		}
	}
}
