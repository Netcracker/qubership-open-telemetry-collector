package utils

import (
	"reflect"
	"testing"
)

func TestMapHelpers(t *testing.T) {
	m := map[string]string{"b": "2", "a": "1"}

	if got := MapToString(m); got != "a=\"1\",b=\"2\"," {
		t.Fatalf("unexpected MapToString result: %q", got)
	}

	keys := GetKeys(m)
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(keys))
	}

	ordered := GetOrderedMapValues(m, []string{"a", "b"})
	if !reflect.DeepEqual(ordered, []string{"1", "2"}) {
		t.Fatalf("unexpected ordered values: %#v", ordered)
	}

	floatValues := GetOrderedMapValuesFloat64Uint64(map[float64]uint64{1.5: 7, 2.5: 9}, []float64{2.5, 1.5})
	if !reflect.DeepEqual(floatValues, []uint64{9, 7}) {
		t.Fatalf("unexpected float ordered values: %#v", floatValues)
	}
}

func TestArrayAndAverageHelpers(t *testing.T) {
	if idx := FindStringIndexInArray([]string{"x", "y", "z"}, "y"); idx != 1 {
		t.Fatalf("expected index 1, got %d", idx)
	}
	if idx := FindStringIndexInArray([]string{"x", "y", "z"}, "missing"); idx != -1 {
		t.Fatalf("expected -1, got %d", idx)
	}

	if avg := GetAverage(nil); avg != 0 {
		t.Fatalf("expected zero average for empty slice, got %v", avg)
	}
	if avg := GetAverage([]float64{1, 2, 3, 4}); avg != 2.5 {
		t.Fatalf("expected 2.5 average, got %v", avg)
	}
}

func TestRemoveIDsFromURIAndPredicates(t *testing.T) {
	uri := "/orders/123/users/550e8400-e29b-41d4-a716-446655440000/tickets/abc9999XYZ/run-12345678"
	got := RemoveIDsFromURI(uri, "_UUID_", "_NUMBER_", "_ID_", 4, "_FSM_", 8)
	want := "/orders/_NUMBER_/users/_UUID_/tickets/_ID_/_ID_"
	if got != want {
		t.Fatalf("unexpected sanitized uri: %q", got)
	}

	if !isUUID("550e8400-e29b-41d4-a716-446655440000") {
		t.Fatal("expected valid UUID")
	}
	if isUUID("not-a-uuid") {
		t.Fatal("did not expect UUID")
	}

	numberCases := map[string]bool{
		"42":    true,
		"-42":   true,
		"+42":   true,
		"":      false,
		"-":     false,
		"12a":   false,
		"1.23":  false,
		" 123 ": false,
	}
	for input, want := range numberCases {
		if got := isNumber(input); got != want {
			t.Fatalf("isNumber(%q) = %v, want %v", input, got, want)
		}
	}

	if !IsID("ab12cd34", 4) {
		t.Fatal("expected ID match")
	}
	if IsID("abc", 2) {
		t.Fatal("did not expect ID match")
	}

	if !IsIdFSM("CamelCase-123", 8) {
		t.Fatal("expected FSM ID match")
	}
	if IsIdFSM("plainword", 8) {
		t.Fatal("did not expect FSM ID match")
	}
	if IsIdFSM("trailing-", 2) {
		t.Fatal("did not expect trailing delimiter to pass FSM match")
	}
	if !IsIdFSM("1", 1) {
		t.Fatal("expected digit-heavy value to pass FSM match")
	}
}
