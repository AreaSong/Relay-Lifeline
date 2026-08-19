package proxy

import (
	"bytes"
	"errors"
	"io"
	"os"
	"sync"

	"github.com/areasong/relay-lifeline/internal/disk"
	"github.com/areasong/relay-lifeline/internal/l10n"
)

var (
	errResponseBodyTooLarge = errors.New("upstream response body exceeds configured limit")
	errCacheBudgetExceeded  = errors.New("response cache budget exceeded")
	errCacheDiskSpace       = errors.New("response cache disk reserve reached")
	errReplayBufferClosed   = errors.New("response cache is closed")
)

const diskCheckWindow int64 = 8 << 20

type cacheBudget struct {
	mu   sync.Mutex
	used int64
}

func (b *cacheBudget) reserve(size, limit int64) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if limit > 0 && (size > limit || b.used > limit-size) {
		return errCacheBudgetExceeded
	}
	b.used += size
	return nil
}

func (b *cacheBudget) release(size int64) {
	b.mu.Lock()
	b.used -= size
	if b.used < 0 {
		b.used = 0
	}
	b.mu.Unlock()
}

type ReplayBuffer struct {
	memoryLimit int64
	maxSize     int64
	maxTotal    int64
	minimumFree int64
	tempDir     string
	budget      *cacheBudget
	memory      bytes.Buffer
	file        *os.File
	size        int64
	reserved    int64
	diskAllowed int64
	closed      bool
}

func NewReplayBuffer(memoryLimit int64, tempDir string) *ReplayBuffer {
	return &ReplayBuffer{memoryLimit: memoryLimit, tempDir: tempDir}
}

func newLimitedReplayBuffer(memoryLimit, maxSize, maxTotal, minimumFree int64, tempDir string, budget *cacheBudget) *ReplayBuffer {
	return &ReplayBuffer{
		memoryLimit: memoryLimit, maxSize: maxSize, maxTotal: maxTotal,
		minimumFree: minimumFree, tempDir: tempDir, budget: budget,
	}
}

func (b *ReplayBuffer) Write(data []byte) (int, error) {
	if b.closed {
		return 0, errReplayBufferClosed
	}
	if len(data) == 0 {
		return 0, nil
	}
	size := int64(len(data))
	if b.maxSize > 0 && (size > b.maxSize || b.size > b.maxSize-size) {
		return 0, errResponseBodyTooLarge
	}
	if b.budget != nil {
		if err := b.budget.reserve(size, b.maxTotal); err != nil {
			return 0, err
		}
	}
	if b.file == nil && b.size+int64(len(data)) > b.memoryLimit {
		if err := b.spill(); err != nil {
			b.release(size)
			return 0, err
		}
	}
	if b.file != nil {
		if err := b.consumeDiskAllowance(size); err != nil {
			b.release(size)
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
	b.reserved += int64(n)
	b.release(size - int64(n))
	return n, err
}

func (b *ReplayBuffer) spill() error {
	if err := b.ensureDiskSpace(int64(b.memory.Len())); err != nil {
		return err
	}
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

func (b *ReplayBuffer) ensureDiskSpace(required int64) error {
	directory := b.tempDir
	if directory == "" {
		directory = os.TempDir()
	}
	available, err := disk.AvailableBytes(directory)
	if err != nil {
		return err
	}
	if available < required || available-required < b.minimumFree {
		return errCacheDiskSpace
	}
	return nil
}

func (b *ReplayBuffer) consumeDiskAllowance(size int64) error {
	if size <= b.diskAllowed {
		b.diskAllowed -= size
		return nil
	}
	check := max(size, diskCheckWindow)
	if b.maxSize > 0 {
		check = min(check, b.maxSize-b.size)
	}
	if err := b.ensureDiskSpace(check); err != nil {
		return err
	}
	b.diskAllowed = check - size
	return nil
}

func (b *ReplayBuffer) release(size int64) {
	if size > 0 && b.budget != nil {
		b.budget.release(size)
	}
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
	if b.closed {
		return nil
	}
	b.closed = true
	b.release(b.reserved)
	b.reserved = 0
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
