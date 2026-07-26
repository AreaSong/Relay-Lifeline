package capture

import (
	"bufio"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const (
	objectMagic = "RLC1"
	chunkSize   = 1 << 20
)

func parseMasterKey(value string) ([]byte, error) {
	if value == "" {
		return nil, errors.New("RELAY_LIFELINE_CAPTURE_KEY is not configured")
	}
	key, err := base64.RawStdEncoding.DecodeString(value)
	if err != nil || len(key) != 32 {
		key, err = base64.StdEncoding.DecodeString(value)
	}
	if err != nil || len(key) != 32 {
		return nil, errors.New("RELAY_LIFELINE_CAPTURE_KEY must be a base64 encoded 32-byte key")
	}
	return key, nil
}

func randomKey() ([]byte, error) {
	key := make([]byte, 32)
	_, err := io.ReadFull(rand.Reader, key)
	return key, err
}

func wrapKey(masterKey, dataKey []byte) (string, error) {
	gcm, err := newGCM(masterKey)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nil, nonce, dataKey, []byte("relay-lifeline-capture-key"))
	return base64.RawStdEncoding.EncodeToString(append(nonce, sealed...)), nil
}

func unwrapKey(masterKey []byte, wrapped string) ([]byte, error) {
	data, err := base64.RawStdEncoding.DecodeString(wrapped)
	if err != nil {
		return nil, err
	}
	gcm, err := newGCM(masterKey)
	if err != nil {
		return nil, err
	}
	if len(data) < gcm.NonceSize() {
		return nil, errors.New("invalid wrapped capture key")
	}
	return gcm.Open(nil, data[:gcm.NonceSize()], data[gcm.NonceSize():], []byte("relay-lifeline-capture-key"))
}

func encryptChunks(key []byte, destination io.Writer, source io.Reader, limit int64) (original, stored int64, truncated bool, err error) {
	gcm, err := newGCM(key)
	if err != nil {
		return 0, 0, false, err
	}
	if _, err = io.WriteString(destination, objectMagic); err != nil {
		return 0, 0, false, err
	}
	buffer := make([]byte, chunkSize)
	for original < limit {
		readLimit := min(int64(len(buffer)), limit-original)
		n, readErr := source.Read(buffer[:readLimit])
		if n > 0 {
			original += int64(n)
			nonce := make([]byte, gcm.NonceSize())
			if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
				return original, stored, false, err
			}
			sealed := gcm.Seal(nil, nonce, buffer[:n], nil)
			if err = binary.Write(destination, binary.BigEndian, uint32(len(sealed))); err != nil {
				return original, stored, false, err
			}
			if _, err = destination.Write(nonce); err != nil {
				return original, stored, false, err
			}
			if _, err = destination.Write(sealed); err != nil {
				return original, stored, false, err
			}
			stored += int64(4 + len(nonce) + len(sealed))
		}
		if readErr == io.EOF {
			return original, stored + int64(len(objectMagic)), false, nil
		}
		if readErr != nil {
			return original, stored, false, readErr
		}
	}
	probe := make([]byte, 1)
	n, readErr := source.Read(probe)
	if readErr != nil && readErr != io.EOF {
		return original, stored, false, readErr
	}
	return original + int64(n), stored + int64(len(objectMagic)), n > 0, nil
}

func decryptChunks(key []byte, source io.Reader, destination io.Writer) error {
	gcm, err := newGCM(key)
	if err != nil {
		return err
	}
	reader := bufio.NewReader(source)
	magic := make([]byte, len(objectMagic))
	if _, err := io.ReadFull(reader, magic); err != nil || string(magic) != objectMagic {
		return errors.New("invalid encrypted capture object")
	}
	for {
		var length uint32
		if err := binary.Read(reader, binary.BigEndian, &length); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if length > chunkSize+uint32(gcm.Overhead()) {
			return fmt.Errorf("invalid encrypted chunk length %d", length)
		}
		nonce := make([]byte, gcm.NonceSize())
		sealed := make([]byte, length)
		if _, err := io.ReadFull(reader, nonce); err != nil {
			return err
		}
		if _, err := io.ReadFull(reader, sealed); err != nil {
			return err
		}
		plain, err := gcm.Open(nil, nonce, sealed, nil)
		if err != nil {
			return err
		}
		if _, err := destination.Write(plain); err != nil {
			return err
		}
	}
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
