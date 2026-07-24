package uplink

import "testing"

func TestRFC8785ContractGoldenVector(t *testing.T) {
	input := []byte(`{"中文":"值","z":0,"nested":{"甲":1,"乙":2},"a":9007199254740991}`)
	canonical, err := CanonicalizeJSON(input)
	if err != nil {
		t.Fatal(err)
	}
	wantCanonical := `{"a":9007199254740991,"nested":{"乙":2,"甲":1},"z":0,"中文":"值"}`
	if string(canonical) != wantCanonical {
		t.Fatalf("canonical JSON\n got: %s\nwant: %s", canonical, wantCanonical)
	}
}

func TestReliableDigestExcludesOnlyAttemptFields(t *testing.T) {
	first := []byte(`{"type":"device.snapshot","sentAt":"2026-01-01T00:00:00Z","replayed":false,"payload":{"a":1}}`)
	second := []byte(`{"payload":{"a":1},"replayed":true,"sentAt":"2026-01-02T00:00:00Z","type":"device.snapshot"}`)
	firstDigest, err := ReliableDigest(first)
	if err != nil {
		t.Fatal(err)
	}
	secondDigest, err := ReliableDigest(second)
	if err != nil {
		t.Fatal(err)
	}
	if firstDigest != secondDigest {
		t.Fatalf("attempt fields changed digest: %s != %s", firstDigest, secondDigest)
	}
}

func TestCanonicalizeRejectsUnsafeNumbersAndDuplicateKeys(t *testing.T) {
	for _, input := range [][]byte{
		[]byte(`{"a":9007199254740992}`),
		[]byte(`{"a":1.5}`),
		[]byte(`{"a":1,"a":2}`),
		[]byte(`{"a":"\ud800"}`),
		[]byte(`{"a":"\udc00"}`),
		[]byte(`{"a":"\ud800\u0061"}`),
		{'{', '"', 'a', '"', ':', '"', 0xff, '"', '}'},
	} {
		if _, err := CanonicalizeJSON(input); err == nil {
			t.Fatalf("invalid canonical input unexpectedly passed: %s", input)
		}
	}
}

func TestCanonicalizeAndDigestRejectTrailingJSONValue(t *testing.T) {
	input := []byte(`{"a":1} {"b":2}`)
	if _, err := CanonicalizeJSON(input); err == nil {
		t.Fatal("canonicalizer accepted a trailing JSON value")
	}
	if _, err := ReliableDigest(input); err == nil {
		t.Fatal("digest accepted a trailing JSON value")
	}
}
