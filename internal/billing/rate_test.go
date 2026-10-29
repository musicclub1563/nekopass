package billing

import (
	"encoding/json"
	"testing"
)

func TestRateUsesExactDecimalJSON(t *testing.T) {
	for raw, want := range map[string]int64{"0": 0, "1": 10000, "0.0001": 1, "1.2345": 12345, "1000.0000": 10000000} {
		r, err := Parse(raw)
		if err != nil || int64(r) != want {
			t.Fatal(raw, r, err)
		}
		data, _ := json.Marshal(r)
		var again Rate
		if err = json.Unmarshal(data, &again); err != nil || again != r {
			t.Fatal("rate serialization lost precision", raw, err)
		}
	}
	for _, raw := range []string{"", "-1", "NaN", "1e3", "0.00001", "1000.0001", "1.2.3"} {
		if _, err := Parse(raw); err == nil {
			t.Fatal("invalid rate accepted", raw)
		}
	}
}
