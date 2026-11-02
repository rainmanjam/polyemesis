package offload

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// TestStep is one step of a target check.
type TestStep struct {
	Step  string `json:"step"` // put | get | delete
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	// Skipped is set on steps that did not run because an earlier one failed.
	Skipped bool `json:"skipped,omitempty"`
}

// TestResult is the Test button's answer.
type TestResult struct {
	OK    bool       `json:"ok"`
	Key   string     `json:"key,omitempty"`
	Steps []TestStep `json:"steps"`
	// FailedStep names the first step that failed, "" when all passed.
	FailedStep string `json:"failedStep,omitempty"`
}

// Probe writes a small object under the target's prefix, reads it back and
// compares it byte for byte, then deletes it, and reports which step failed.
// The delete runs whenever the put succeeded, so a failed get does not leave
// a stray object behind.
func Probe(ctx context.Context, t db.StorageTarget, newClient func(db.StorageTarget) (Client, error)) TestResult {
	if newClient == nil {
		newClient = NewClient
	}
	res := TestResult{}
	fail := func(step string, err error) {
		res.Steps = append(res.Steps, TestStep{Step: step, Error: clipErr(err)})
		if res.FailedStep == "" {
			res.FailedStep = step
		}
	}
	skip := func(steps ...string) {
		for _, s := range steps {
			res.Steps = append(res.Steps, TestStep{Step: s, Skipped: true})
		}
	}
	c, err := newClient(t)
	if err != nil {
		fail("put", err)
		skip("get", "delete")
		return res
	}
	var nonce [8]byte
	_, _ = rand.Read(nonce[:])
	key, err := ObjectKey(t.PrefixTemplate, "test", nil, time.Now(), ".polyemesis-offload-test-"+hex.EncodeToString(nonce[:]))
	if err != nil {
		fail("put", err)
		skip("get", "delete")
		return res
	}
	res.Key = key
	payload := []byte("polyemesis storage target check " + hex.EncodeToString(nonce[:]) + "\n")
	sh := sha256.Sum256(payload)
	mb := md5.Sum(payload)
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	if _, err := c.Put(ctx, key, bytes.NewReader(payload), int64(len(payload)), hex.EncodeToString(sh[:]),
		base64.StdEncoding.EncodeToString(mb[:]), nil); err != nil {
		fail("put", err)
		skip("get", "delete")
		return res
	}
	res.Steps = append(res.Steps, TestStep{Step: "put", OK: true})

	if body, _, err := c.Get(ctx, key); err != nil {
		fail("get", err)
	} else {
		got, rerr := io.ReadAll(io.LimitReader(body, 1<<16))
		_ = body.Close()
		switch {
		case rerr != nil:
			fail("get", rerr)
		case !bytes.Equal(got, payload):
			fail("get", errors.New("the object read back is not the object written"))
		default:
			res.Steps = append(res.Steps, TestStep{Step: "get", OK: true})
		}
	}
	if err := c.Delete(ctx, key); err != nil {
		fail("delete", err)
	} else {
		res.Steps = append(res.Steps, TestStep{Step: "delete", OK: true})
	}
	res.OK = res.FailedStep == ""
	return res
}
