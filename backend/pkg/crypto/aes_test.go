package crypto

import (
	"testing"

	"pgregory.net/rapid"
)

// Property 8: Encryption Round-Trip
func TestPropertyEncryptionRoundTrip(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		plaintext := rapid.StringN(1, 500, -1).Draw(t, "plaintext")

		encrypted, err := Encrypt(plaintext)
		if err != nil {
			t.Fatalf("Encrypt failed: %v", err)
		}

		if encrypted == plaintext {
			t.Fatal("Encrypted should differ from plaintext")
		}

		decrypted, err := Decrypt(encrypted)
		if err != nil {
			t.Fatalf("Decrypt failed: %v", err)
		}

		if decrypted != plaintext {
			t.Fatalf("Round-trip failed: got %q, want %q", decrypted, plaintext)
		}
	})
}

// Property: Different plaintexts produce different ciphertexts (with high probability)
func TestPropertyEncryptionUniqueness(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		a := rapid.StringN(1, 100, -1).Draw(t, "a")
		b := rapid.StringN(1, 100, -1).Draw(t, "b")

		if a == b {
			return // skip identical inputs
		}

		encA, _ := Encrypt(a)
		encB, _ := Encrypt(b)

		if encA == encB {
			t.Fatalf("Different plaintexts produced same ciphertext")
		}
	})
}
