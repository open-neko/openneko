package webfront

import "testing"

func TestClaimsNothingReadsTheProseOfTheAnswer(t *testing.T) {
	for _, s := range []string{
		"There is currently no inventory recorded in the Tool Crib location.",
		"There were no distinct buyers or sales recorded in Q1 2025.",
		"We found no orders for that customer.",
	} {
		if !claimsNothing(s) {
			t.Errorf("missed: %q", s)
		}
	}
	for _, s := range []string{
		"Hex Nut 2 has the most units, 657.",
		"Revenue rose 9.1%.\n```neko_vitals\n{\"sub\": \"No inventory recorded\"}\n```",
	} {
		if claimsNothing(s) {
			t.Errorf("false claim: %q", s)
		}
	}
}

func TestShowsNothingFindsAnEmptyListOrAZero(t *testing.T) {
	cases := map[string]bool{
		`{"data":{"a":[]}}`:                                           true,
		`{"data":{"a":{"count_id":0}}}`:                               true,
		`{"data":{"a":[{"n":4,"max_d":null}]}}`:                       true,
		`{"data":{"location":[{"locationid":1,"name":"Tool Crib"}]}}`: false,
		`{"errors":[{"message":"x"}]}`:                                false,
	}
	for text, want := range cases {
		result, _ := jsParse([]byte(`{"content":[{"type":"text","text":` + jsStringify(text, false) + `}]}`))
		if got := resultShowsNothing(result); got != want {
			t.Errorf("%s: got %v", text, got)
		}
	}
}

func TestUnsupportedNothingNeedsAQueryAndNoEmptyResult(t *testing.T) {
	st := &chatTurnState{}
	res := axRunResult{FinalText: "There is no inventory in the Tool Crib."}
	if st.unsupportedNothing(res) {
		t.Fatal("a run with no query is not checked")
	}
	st.executes = map[string]bool{"c1": true}
	if !st.unsupportedNothing(res) {
		t.Fatal("a claim with no empty result must be checked")
	}
	st.sawNothing = true
	if st.unsupportedNothing(res) {
		t.Fatal("an empty result supports the claim")
	}
}
