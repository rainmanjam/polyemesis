package api

import (
	"errors"
	"net/http"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/config"
)

// ampleDisk is the free-space reading every fixture that is NOT about disk
// space uses. The upload floor is UploadFreeFloor + MaxUploadBytes (10 GiB),
// so a test that measured the host volume answered 507 on any runner that
// happened to be low on disk. Large enough to clear the floor, and a constant,
// so the answer is the same on every machine.
func ampleDisk(string) (uint64, error) { return 1 << 50, nil }

func uploadWithFree(t *testing.T, free func(string) (uint64, error)) int {
	t.Helper()
	_, h, _ := testServerWith(t, Options{
		Config:          config.Config{DataDir: t.TempDir()},
		UploadFreeBytes: free,
	})
	r := uploadRequest(t, "file", "clip.mp4", "pretend media bytes")
	login(t, h)(r)
	return do(t, h, r).Code
}

// The seam is the reading itself: a volume with less than the floor refuses,
// and one with more accepts, whatever the host really has.
func TestUploadFloorFollowsTheInjectedFreeSpace(t *testing.T) {
	need := uint64(UploadFreeFloor) + uint64(MaxUploadBytes)
	cases := []struct {
		name string
		free func(string) (uint64, error)
		want int
	}{
		{"one byte short of the floor", func(string) (uint64, error) { return need - 1, nil }, http.StatusInsufficientStorage},
		{"exactly the floor", func(string) (uint64, error) { return need, nil }, http.StatusCreated},
		{"a reading that fails closes the gate", func(string) (uint64, error) { return 0, errors.New("statfs failed") }, http.StatusInsufficientStorage},
		{"ample", ampleDisk, http.StatusCreated},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := uploadWithFree(t, c.free); got != c.want {
				t.Fatalf("status = %d, want %d", got, c.want)
			}
		})
	}
}
