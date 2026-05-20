package marketplace

import "testing"

func TestCompareNumeric(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.0.0", "1.0.0", 0},
		{"2.0.0", "1.0.0", 1},
		{"1.0.0", "2.0.0", -1},
		{"1.2.0", "1.1.9", 1},
		{"1.1.10", "1.1.2", 1},
	}
	for _, c := range cases {
		got := Compare(c.a, c.b)
		if sign(got) != sign(c.want) {
			t.Fatalf("Compare(%s,%s)=%d want sign %d", c.a, c.b, got, c.want)
		}
	}
}

func TestComparePrerelease(t *testing.T) {
	if Compare("1.0.0", "1.0.0-rc1") <= 0 {
		t.Fatal("non-pre should beat pre")
	}
	if Compare("1.0.0-rc1", "1.0.0") >= 0 {
		t.Fatal("pre should lose to non-pre")
	}
	if Compare("1.0.0-rc2", "1.0.0-rc1") <= 0 {
		t.Fatal("rc2 > rc1 lexicographically")
	}
}

func TestParsePanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on invalid semver")
		}
	}()
	mustParse("not-a-semver")
}

func sign(x int) int {
	switch {
	case x > 0:
		return 1
	case x < 0:
		return -1
	}
	return 0
}
