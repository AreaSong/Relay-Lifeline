package proxy

import (
	"bytes"
	"io"
	"os"

	"github.com/areasong/relay-lifeline/internal/l10n"
)

type ReplayBuffer struct {
	memoryLimit int64
	tempDir     string
	memory      bytes.Buffer
	file        *os.File
	size        int64
}

func NewReplayBuffer(memoryLimit int64, tempDir string) *ReplayBuffer {
	return &ReplayBuffer{memoryLimit: memoryLimit, tempDir: tempDir}
}

func (b *ReplayBuffer) Write(data []byte) (int, error) {
	if b.file == nil && b.size+int64(len(data)) > b.memoryLimit {
		if err := b.spill(); err != nil {
			return 0, err
		}
	}
	var n int
	var err error
	if b.file != nil {
		n, err = b.file.Write(data)
	} else {
		n, err = b.memory.Write(data)
	}
	b.size += int64(n)
	return n, err
}

func (b *ReplayBuffer) spill() error {
	file, err := os.CreateTemp(b.tempDir, "relay-lifeline-response-*")
	if err != nil {
		return l10n.E("proxy.cache_create_failed", err)
	}
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		os.Remove(file.Name())
		return err
	}
	if _, err := file.Write(b.memory.Bytes()); err != nil {
		file.Close()
		os.Remove(file.Name())
		return err
	}
	b.memory.Reset()
	b.file = file
	return nil
}

func (b *ReplayBuffer) Reader() (io.ReadCloser, error) {
	if b.file == nil {
		return io.NopCloser(bytes.NewReader(b.memory.Bytes())), nil
	}
	if err := b.file.Sync(); err != nil {
		return nil, err
	}
	return os.Open(b.file.Name())
}

func (b *ReplayBuffer) WriteTo(writer io.Writer) (int64, error) {
	reader, err := b.Reader()
	if err != nil {
		return 0, err
	}
	defer reader.Close()
	return io.Copy(writer, reader)
}

func (b *ReplayBuffer) Size() int64 { return b.size }

func (b *ReplayBuffer) Close() error {
	if b.file == nil {
		return nil
	}
	name := b.file.Name()
	err := b.file.Close()
	removeErr := os.Remove(name)
	if err != nil {
		return err
	}
	return removeErr
}
