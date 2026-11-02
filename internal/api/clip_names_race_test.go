package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/clipper"
)

// THE SAME SHORT SUBMITTED AT ONCE GETS A NAME PER JOB. Six identical exports
// posted together -- a double-click, two tabs, a script -- each queue their own
// file: choosing a free name and queueing the job that holds it are one step,
// so no two of them can both see the same name as free.
//
// MUTATION: the reservation lock is not taken. Observed: FAIL, six jobs share
// one path.
func TestConcurrentShortsOfOneSpanGetAFileEach(t *testing.T) {
	h, sign, srv, rec := highlightFixture(t)
	body, _ := json.Marshal(map[string]any{"inMs": 10_000, "outMs": 40_000, "title": "same",
		"vertical": map[string]any{"layout": "center"}})
	const n = 6
	ids := make([]int64, n)
	codes := make([]int, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := httptest.NewRequest(http.MethodPost, recPath(rec, "/export"), bytes.NewReader(body))
			r.Header.Set("Content-Type", "application/json")
			sign(r)
			<-start
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			codes[i] = w.Code
			var job struct {
				ID int64 `json:"id"`
			}
			_ = json.Unmarshal(w.Body.Bytes(), &job)
			ids[i] = job.ID
		}(i)
	}
	close(start)
	wg.Wait()
	paths := map[string]int64{}
	for i := 0; i < n; i++ {
		if codes[i] != http.StatusAccepted {
			t.Fatalf("export %d answered %d", i, codes[i])
		}
		j, err := srv.store.GetJob(ids[i])
		if err != nil {
			t.Fatal(err)
		}
		var p clipper.JobParams
		if err := json.Unmarshal(j.Params, &p); err != nil {
			t.Fatal(err)
		}
		if other, dup := paths[p.Request.OutPath]; dup {
			t.Errorf("jobs %d and %d both write %s", other, ids[i], p.Request.OutPath)
		}
		paths[p.Request.OutPath] = ids[i]
	}
}
