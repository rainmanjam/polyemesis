package objstore

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// fakeS3 is a small in-memory S3 wire server: path-style PUT, HEAD, GET and
// DELETE of objects, bucket creation, and the multipart verbs, with the ETags
// a real service gives (MD5, and md5-of-part-md5s-N for multipart). It does
// not check signatures -- the live suite does that -- it checks that the
// client speaks the protocol.
type fakeS3 struct {
	mu      sync.Mutex
	objs    map[string][]byte
	etags   map[string]string
	meta    map[string]http.Header
	uploads map[string]map[int][]byte
	buckets map[string]bool
	next    int
	// completeError makes CompleteMultipartUpload answer 200 with an <Error>.
	completeError bool
	seen          []string // "METHOD path?query"
}

func newFakeS3(t *testing.T) (*fakeS3, *httptest.Server) {
	f := &fakeS3{objs: map[string][]byte{}, etags: map[string]string{}, meta: map[string]http.Header{},
		uploads: map[string]map[int][]byte{}, buckets: map[string]bool{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, srv
}

func s3Error(w http.ResponseWriter, status int, code string) {
	w.WriteHeader(status)
	fmt.Fprintf(w, "<Error><Code>%s</Code><Message>fake</Message></Error>", code)
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	q := r.URL.Query()
	f.seen = append(f.seen, r.Method+" "+r.URL.EscapedPath()+"?"+r.URL.RawQuery)
	path := strings.TrimPrefix(r.URL.Path, "/")
	if !strings.Contains(path, "/") { // bucket
		if r.Method == http.MethodPut {
			if f.buckets[path] {
				s3Error(w, 409, "BucketAlreadyOwnedByYou")
				return
			}
			f.buckets[path] = true
			return
		}
		s3Error(w, 405, "MethodNotAllowed")
		return
	}
	body, _ := io.ReadAll(r.Body)
	switch {
	case r.Method == http.MethodPost && q.Has("uploads"):
		f.next++
		id := "upload-" + strconv.Itoa(f.next)
		f.uploads[id] = map[int][]byte{}
		f.meta[path] = r.Header.Clone()
		fmt.Fprintf(w, "<InitiateMultipartUploadResult><UploadId>%s</UploadId></InitiateMultipartUploadResult>", id)
	case r.Method == http.MethodPut && q.Has("uploadId"):
		up, ok := f.uploads[q.Get("uploadId")]
		if !ok {
			s3Error(w, 404, "NoSuchUpload")
			return
		}
		n, _ := strconv.Atoi(q.Get("partNumber"))
		up[n] = body
		sum := md5.Sum(body)
		w.Header().Set("ETag", `"`+hex.EncodeToString(sum[:])+`"`)
	case r.Method == http.MethodPost && q.Has("uploadId"):
		up, ok := f.uploads[q.Get("uploadId")]
		if !ok {
			s3Error(w, 404, "NoSuchUpload")
			return
		}
		if f.completeError {
			fmt.Fprint(w, "<Error><Code>InternalError</Code><Message>try again</Message></Error>")
			return
		}
		var req struct {
			Parts []struct {
				N int `xml:"PartNumber"`
			} `xml:"Part"`
		}
		_ = xml.Unmarshal(body, &req)
		nums := []int{}
		for _, p := range req.Parts {
			nums = append(nums, p.N)
		}
		sort.Ints(nums)
		var all []byte
		h := md5.New()
		for _, n := range nums {
			all = append(all, up[n]...)
			s := md5.Sum(up[n])
			h.Write(s[:])
		}
		f.objs[path] = all
		f.etags[path] = hex.EncodeToString(h.Sum(nil)) + "-" + strconv.Itoa(len(nums))
		delete(f.uploads, q.Get("uploadId"))
		fmt.Fprintf(w, `<CompleteMultipartUploadResult><ETag>"%s"</ETag></CompleteMultipartUploadResult>`, f.etags[path])
	case r.Method == http.MethodDelete && q.Has("uploadId"):
		if _, ok := f.uploads[q.Get("uploadId")]; !ok {
			s3Error(w, 404, "NoSuchUpload")
			return
		}
		delete(f.uploads, q.Get("uploadId"))
		w.WriteHeader(204)
	case r.Method == http.MethodPut:
		f.objs[path] = body
		sum := md5.Sum(body)
		f.etags[path] = hex.EncodeToString(sum[:])
		f.meta[path] = r.Header.Clone()
		w.Header().Set("ETag", `"`+f.etags[path]+`"`)
	case r.Method == http.MethodHead || r.Method == http.MethodGet:
		b, ok := f.objs[path]
		if !ok {
			s3Error(w, 404, "NoSuchKey")
			return
		}
		for k, v := range f.meta[path] {
			if strings.HasPrefix(strings.ToLower(k), "x-amz-meta-") {
				w.Header()[k] = v
			}
		}
		w.Header().Set("ETag", `"`+f.etags[path]+`"`)
		w.Header().Set("Content-Length", strconv.Itoa(len(b)))
		if r.Method == http.MethodGet {
			_, _ = io.Copy(w, bytes.NewReader(b))
		}
	case r.Method == http.MethodDelete:
		delete(f.objs, path)
		w.WriteHeader(204)
	default:
		s3Error(w, 400, "Unsupported")
	}
}
