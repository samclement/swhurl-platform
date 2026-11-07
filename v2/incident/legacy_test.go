package incident

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

// These frozen evaluations were captured BEFORE implementing the model, by running the 40 applicable legacy
// tests. Two artificially sparse pending entries are normalized as documented in testdata/README.md; genuine
// inputs/answers are unchanged. Test names preserve the case map; no check regenerates the fixture.
func TestLegacyEvaluations(t *testing.T) {
	raw, err := os.ReadFile("testdata/legacy.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Test     string
		Previous json.RawMessage
		Snapshot Snapshot
		Now      float64
		Base     string
		Copies   bool
		Expected Object
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.Test, func(t *testing.T) {
			var previous *State
			if string(tc.Previous) != "null" {
				previous, err = DecodeState(string(tc.Previous))
				if err != nil {
					t.Fatal(err)
				}
			}
			before, _ := json.Marshal(tc.Snapshot)
			var original string
			if previous != nil {
				original, _ = previous.Encode()
			}
			result, err := Evaluate(previous, tc.Snapshot, tc.Now, tc.Base, tc.Copies)
			if err != nil {
				t.Fatal(err)
			}
			var got Object
			encoded, err := result.Encode()
			if err != nil {
				t.Fatal(err)
			}
			_ = json.Unmarshal([]byte(encoded), &got)
			if !reflect.DeepEqual(got, tc.Expected) {
				t.Errorf("got %s\nwant %s", encoded, mustJSON(tc.Expected))
			}
			after, _ := json.Marshal(tc.Snapshot)
			if string(before) != string(after) {
				t.Fatal("snapshot mutated")
			}
			if previous != nil {
				after, _ := previous.Encode()
				if original != after {
					t.Fatal("previous mutated")
				}
			}
		})
	}
}
func mustJSON(v any) string { raw, _ := json.Marshal(v); return string(raw) }
