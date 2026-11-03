package publish

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/clipper"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/jobs"
)

type fakeJobs map[int64]*jobs.Job

func (f fakeJobs) GetJob(id int64) (*jobs.Job, error) {
	if j, ok := f[id]; ok {
		return j, nil
	}
	if f == nil {
		return nil, errors.New("the database is busy")
	}
	return nil, db.ErrNotFound
}

func exportJob(t *testing.T, dir, name string, state jobs.State, kind jobs.Kind) (fakeJobs, string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("MP4"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, _ := json.Marshal(clipper.JobResult{Path: path, Seconds: 12, Vertical: true})
	return fakeJobs{1: {ID: 1, Kind: kind, State: state, Result: res}}, path
}

func TestResolveClipFindsAFinishedVerticalExport(t *testing.T) {
	dir := t.TempDir()
	store, path := exportJob(t, dir, "a.mp4", jobs.StateDone, clipper.JobKind)
	f, err := ResolveClip(store, dir, 1)
	if err != nil || f.Path != path || f.Size != 3 || f.Seconds != 12 || !f.Vertical {
		t.Fatalf("file = %+v, %v", f, err)
	}
	got, err := ClipsFrom(store, dir)(context.Background(), 1)
	if err != nil || got.Path != path {
		t.Fatalf("ClipsFrom = %+v, %v", got, err)
	}
}

func TestResolveClipRefusesWhatIsNotAFinishedExport(t *testing.T) {
	dir := t.TempDir()
	store, _ := exportJob(t, dir, "a.mp4", jobs.StateRunning, clipper.JobKind)
	if _, err := ResolveClip(store, dir, 1); !errors.Is(err, ErrNotAClip) {
		t.Errorf("a running export = %v", err)
	}
	other, _ := exportJob(t, dir, "b.mp4", jobs.StateDone, "transcribe")
	if _, err := ResolveClip(other, dir, 1); !errors.Is(err, ErrNotAClip) {
		t.Errorf("another kind = %v", err)
	}
	if _, err := ResolveClip(store, dir, 99); !errors.Is(err, ErrClipGone) {
		t.Errorf("a missing job = %v", err)
	}
}

func TestResolveClipConfinesTheFileToTheExportsDirectory(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	store, _ := exportJob(t, outside, "evil.mp4", jobs.StateDone, clipper.JobKind)
	if _, err := ResolveClip(store, dir, 1); err == nil {
		t.Fatal("a file outside the exports directory was resolved")
	}
}

func TestResolveClipReportsAFileThatWasDeleted(t *testing.T) {
	dir := t.TempDir()
	store, path := exportJob(t, dir, "a.mp4", jobs.StateDone, clipper.JobKind)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveClip(store, dir, 1); !errors.Is(err, ErrClipGone) {
		t.Fatalf("err = %v, want ErrClipGone", err)
	}
}

func TestGrantFailureKnowsEachPlatformsScopes(t *testing.T) {
	cases := []struct {
		platform, scopes string
		ok               bool
	}{
		{PlatformYouTube, "https://www.googleapis.com/auth/youtube", true},
		{PlatformYouTube, "https://www.googleapis.com/auth/youtube.upload", true},
		{PlatformYouTube, "https://www.googleapis.com/auth/youtube.readonly", false},
		{PlatformYouTube, "", true}, // the platform reported nothing: no verdict
		{PlatformTikTok, "user.info.basic,video.publish", true},
		{PlatformTikTok, "user.info.basic", false},
		{PlatformX, "tweet.write media.write", true},
		{PlatformX, "tweet.read", false},
		{PlatformX, "", false},
	}
	for _, c := range cases {
		if got := GrantFailure(c.platform, c.scopes) == nil; got != c.ok {
			t.Errorf("GrantFailure(%s, %q) ok = %v, want %v", c.platform, c.scopes, got, c.ok)
		}
	}
	if GrantFailure("myspace", "") == nil {
		t.Error("an unknown platform passed")
	}
}

func TestTokenGateRefusesUntilTheServerIsUp(t *testing.T) {
	var g TokenGate
	if _, err := g.Source()(context.Background(), 1); err == nil {
		t.Fatal("the gate answered before it was set")
	}
	g.Set(func(context.Context, int64) (Credential, error) {
		return Credential{AccessToken: "placeholder-token"}, nil
	})
	c, err := g.Source()(context.Background(), 1)
	if err != nil || c.AccessToken != "placeholder-token" {
		t.Fatalf("credential = %+v, %v", c, err)
	}
}

func TestNormalizeCoversTheOtherPlatforms(t *testing.T) {
	long := make([]byte, 2300)
	for i := range long {
		long[i] = 'a'
	}
	if _, err := Normalize(PlatformTikTok, Meta{Title: string(long)}); err == nil {
		t.Error("an over-long TikTok caption was accepted")
	}
	if m, err := Normalize(PlatformTikTok, Meta{Title: "hi", Tags: []string{"a"}}); err != nil || m.Privacy != TikTokPrivacy {
		t.Errorf("tiktok = %+v, %v", m, err)
	}
	if _, err := Normalize(PlatformX, Meta{Title: string(long[:300])}); err == nil {
		t.Error("an over-long X post was accepted")
	}
	if m, err := Normalize(PlatformX, Meta{Title: "hi"}); err != nil || m.Privacy != PrivacyPublic {
		t.Errorf("x = %+v, %v", m, err)
	}
	if _, err := Normalize(PlatformYouTube, Meta{Title: "a<b"}); err == nil {
		t.Error("< in a YouTube title was accepted")
	}
	tags := make([]string, 31)
	for i := range tags {
		tags[i] = string(rune('a'+i%26)) + string(rune('a'+i/26))
	}
	if _, err := Normalize(PlatformYouTube, Meta{Title: "t", Tags: tags}); err == nil {
		t.Error("31 hashtags were accepted")
	}
}

// A failed read says nothing about the clip: it must not become a permanent
// "the clip is gone".
func TestAFailedJobReadIsNotTheClipBeingGone(t *testing.T) {
	var busy fakeJobs // nil: every read fails
	_, err := ResolveClip(busy, t.TempDir(), 1)
	if err == nil || errors.Is(err, ErrClipGone) || errors.Is(err, ErrNotAClip) {
		t.Fatalf("err = %v, want a retryable read error", err)
	}
}
