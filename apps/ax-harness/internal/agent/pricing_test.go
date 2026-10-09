package agent

import "testing"

func TestTokenPriceConservativeRounding(t *testing.T) {
	p := TokenPrice{InputMicrosPerMillion: 250_000, OutputMicrosPerMillion: 1_000_000}
	if !p.Valid() || p.Reservation(4_096) != 4_096 {
		t.Fatalf("invalid price or reservation: %+v", p)
	}
	// Every component rounds up separately; unexplained tokens use the
	// larger rate, including when total is smaller than the split.
	if got := p.Observed(1, 1, 3); got != 3 {
		t.Fatalf("observed charge = %d, want 3", got)
	}
	if got := p.Observed(4, 1, 2); got != 2 {
		t.Fatalf("split charge = %d, want 2", got)
	}
	if (TokenPrice{InputMicrosPerMillion: -1, OutputMicrosPerMillion: 1}).Valid() {
		t.Fatal("negative price admitted")
	}
}
