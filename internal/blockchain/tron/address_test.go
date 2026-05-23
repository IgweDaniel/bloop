package tron

import "testing"

func TestNormalizeAddressSupportsBase58AndHex(t *testing.T) {
	const base58 = "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"
	const hex41 = "41a614f803b6fd780986a42c78ec9c7f77e6ded13c"

	got, err := NormalizeAddress(base58)
	if err != nil {
		t.Fatalf("NormalizeAddress(base58) returned error: %v", err)
	}
	if got != base58 {
		t.Fatalf("NormalizeAddress(base58) = %s, want %s", got, base58)
	}

	got, err = NormalizeAddress(hex41)
	if err != nil {
		t.Fatalf("NormalizeAddress(hex41) returned error: %v", err)
	}
	if got != base58 {
		t.Fatalf("NormalizeAddress(hex41) = %s, want %s", got, base58)
	}
}

func TestTopicToBase58Address(t *testing.T) {
	got, err := topicToBase58Address("000000000000000000000000a614f803b6fd780986a42c78ec9c7f77e6ded13c")
	if err != nil {
		t.Fatalf("topicToBase58Address returned error: %v", err)
	}
	if got != "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t" {
		t.Fatalf("topicToBase58Address = %s", got)
	}
}
