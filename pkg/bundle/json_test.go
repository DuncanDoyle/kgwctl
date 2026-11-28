package bundle

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSourceOperationJSON(t *testing.T) {
	b, err := json.Marshal(Source{Operation: OpConfigDump})
	if err != nil || !strings.Contains(string(b), `"operation":"config-dump"`) {
		t.Fatalf("got %s, %v", b, err)
	}
	b, _ = json.Marshal(Source{Operation: OpList})
	if !strings.Contains(string(b), `"operation":"list"`) {
		t.Errorf("OpList must render: %s", b)
	}
	b, _ = json.Marshal(Source{})
	if strings.Contains(string(b), "operation") {
		t.Errorf("unset operation must be omitted: %s", b)
	}
	var s Source
	if err := json.Unmarshal([]byte(`{"operation":"previous-log"}`), &s); err != nil || s.Operation != OpPreviousLog {
		t.Errorf("round trip: %v %v", s.Operation, err)
	}
}

func TestZeroTimesOmitted(t *testing.T) {
	for _, v := range []any{Window{Tail: 5}, Replica{Pod: "p"}} {
		b, _ := json.Marshal(v)
		if strings.Contains(string(b), "0001-01-01") {
			t.Errorf("zero time serialized: %s", b)
		}
	}
}
