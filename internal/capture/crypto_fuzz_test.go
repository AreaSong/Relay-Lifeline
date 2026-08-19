package capture

import (
	"bytes"
	"testing"
)

func FuzzEncryptedObjectRoundTrip(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte("capture body"))
	f.Add(bytes.Repeat([]byte{'x'}, chunkSize+1))
	f.Fuzz(func(t *testing.T, input []byte) {
		if len(input) > 2*chunkSize {
			input = input[:2*chunkSize]
		}
		key := bytes.Repeat([]byte{0x37}, 32)
		var encrypted bytes.Buffer
		_, _, _, err := encryptChunks(key, &encrypted, bytes.NewReader(input), int64(chunkSize))
		if err != nil {
			t.Fatal(err)
		}
		var plaintext bytes.Buffer
		if err := decryptChunks(key, bytes.NewReader(encrypted.Bytes()), &plaintext); err != nil {
			t.Fatal(err)
		}
		expected := input
		if len(expected) > chunkSize {
			expected = expected[:chunkSize]
		}
		if !bytes.Equal(plaintext.Bytes(), expected) {
			t.Fatalf("round trip mismatch: got %d want %d bytes", plaintext.Len(), len(expected))
		}
	})
}

func FuzzDecryptChunksRejectsMalformed(f *testing.F) {
	f.Add([]byte(objectMagic))
	f.Add([]byte("not-an-object"))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1024*1024 {
			data = data[:1024*1024]
		}
		var output bytes.Buffer
		_ = decryptChunks(bytes.Repeat([]byte{0x41}, 32), bytes.NewReader(data), &output)
	})
}
