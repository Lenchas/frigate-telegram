package frigate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/oldtyt/frigate-telegram/internal/config"
	"github.com/oldtyt/frigate-telegram/internal/log"
)

type fakeReviewStore struct {
	start                                int64
	paused, muted, failFinish, failState bool
	locks                                map[string]string
	sent                                 map[string]int
}

func newFakeStore() *fakeReviewStore {
	return &fakeReviewStore{start: 1, locks: map[string]string{}, sent: map[string]int{}}
}
func (s *fakeReviewStore) Start(_ context.Context, n int64) (int64, error) {
	if s.start == 0 {
		s.start = n
	}
	return s.start, nil
}
func (s *fakeReviewStore) State(context.Context) (bool, bool, error) {
	if s.failState {
		return false, false, errors.New("redis unavailable")
	}
	return s.paused, s.muted, nil
}
func (s *fakeReviewStore) Claim(_ context.Context, id, token string, _ time.Duration) (bool, error) {
	if s.sent[id] != 0 || s.locks[id] != "" {
		return false, nil
	}
	s.locks[id] = token
	return true, nil
}
func (s *fakeReviewStore) Finish(_ context.Context, id, token string, message int, _ time.Duration) error {
	if s.failFinish {
		return errors.New("redis write unavailable")
	}
	s.sent[id] = message
	delete(s.locks, id)
	return nil
}
func (s *fakeReviewStore) Release(_ context.Context, id, token string) error {
	if s.locks[id] == token {
		delete(s.locks, id)
	}
	return nil
}

func testReview() Review {
	r := Review{ID: "episode-1", Camera: "Garage", StartTime: 1000, EndTime: 1100, Severity: "alert"}
	r.Data.Objects = []string{"person", "car"}
	r.Data.Zones = []string{"garage-zone", "garage-alert-zone"}
	for i := 0; i < 9; i++ {
		r.Data.Detections = append(r.Data.Detections, fmt.Sprintf("object-%d", i))
	}
	return r
}

func newTestService(t *testing.T, handler http.HandlerFunc) (*ReviewService, *fakeReviewStore) {
	t.Helper()
	log.LogFunc()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	conf := config.New()
	conf.FrigateURL = server.URL
	conf.FrigateExternalURL = server.URL
	conf.FrigateIncludeLabel = []string{"person", "car"}
	conf.FrigateIncludeSeverity = []string{"alert"}
	conf.FrigateIncludeZone = []string{"garage-alert-zone"}
	bot, err := tgbotapi.NewBotAPIWithClient("123:test", server.URL+"/bot%s/%s", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewReviewService(conf, bot)
	if err != nil {
		t.Fatal(err)
	}
	store := newFakeStore()
	s.store = store
	s.client = server.Client()
	return s, store
}

func mockGetMe(w http.ResponseWriter) {
	io.WriteString(w, `{"ok":true,"result":{"id":123,"is_bot":true,"first_name":"test","username":"testbot"}}`)
}

func TestReviewOneMessageForNineObjectsAndAcrossRestart(t *testing.T) {
	r := testReview()
	sends := 0
	var clipPath string
	s, store := newTestService(t, func(w http.ResponseWriter, req *http.Request) {
		switch {
		case strings.HasSuffix(req.URL.Path, "getMe"):
			mockGetMe(w)
		case req.URL.Path == "/api/review":
			if req.URL.Query().Get("reviewed") != "1" || req.URL.Query().Has("limit") {
				t.Error("must include reviewed episodes without a pre-filter event limit")
			}
			json.NewEncoder(w).Encode([]Review{r})
		case strings.HasSuffix(req.URL.Path, "clip.mp4"):
			clipPath = req.URL.Path
			io.WriteString(w, "video-data")
		case strings.HasSuffix(req.URL.Path, "sendVideo"):
			sends++
			if err := req.ParseMultipartForm(1024); err != nil {
				t.Error(err)
			}
			if req.MultipartForm != nil {
				defer req.MultipartForm.RemoveAll()
			}
			io.WriteString(w, `{"ok":true,"result":{"message_id":42}}`)
		default:
			t.Errorf("unexpected request %s", req.URL)
			http.NotFound(w, req)
		}
	})
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	for i := 0; i < 2; i++ {
		if err := s.Poll(context.Background(), time.Unix(1200, 0)); err != nil {
			t.Fatal(err)
		}
	}
	restarted, err := NewReviewService(s.conf, s.bot)
	if err != nil {
		t.Fatal(err)
	}
	restarted.store = store
	restarted.client = s.client
	if err = restarted.Poll(context.Background(), time.Unix(1210, 0)); err != nil {
		t.Fatal(err)
	}
	if sends != 1 {
		t.Fatalf("got %d sends for one episode", sends)
	}
	if !strings.Contains(clipPath, "/start/997.000000/end/1027.000000/") {
		t.Fatalf("unbounded clip range: %s", clipPath)
	}
	entries, _ := os.ReadDir(tmp)
	if len(entries) != 0 {
		t.Fatalf("temporary files leaked: %v", entries)
	}
}

func TestReviewOversizedClipFallsBackToOnePhoto(t *testing.T) {
	sends := 0
	r := testReview()
	s, store := newTestService(t, func(w http.ResponseWriter, req *http.Request) {
		switch {
		case strings.HasSuffix(req.URL.Path, "getMe"):
			mockGetMe(w)
		case strings.HasSuffix(req.URL.Path, "clip.mp4"):
			w.Header().Set("Content-Length", "25000001")
			w.WriteHeader(200)
		case strings.HasSuffix(req.URL.Path, "thumbnail.jpg"):
			io.WriteString(w, "photo-data")
		case strings.HasSuffix(req.URL.Path, "sendPhoto"):
			sends++
			io.Copy(io.Discard, req.Body)
			io.WriteString(w, `{"ok":true,"result":{"message_id":43}}`)
		default:
			t.Errorf("unexpected %s", req.URL)
			http.NotFound(w, req)
		}
	})
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	if err := s.deliver(context.Background(), r, time.Unix(1200, 0)); err != nil {
		t.Fatal(err)
	}
	if sends != 1 || store.sent[r.ID] != 43 {
		t.Fatal("fallback was not committed")
	}
	entries, _ := os.ReadDir(tmp)
	if len(entries) != 0 {
		t.Fatal("fallback leaked files")
	}
}

func TestDownloadFailureRemovesPartialFiles(t *testing.T) {
	for _, kind := range []string{"unknown-length-large", "empty", "truncated", "http-error"} {
		t.Run(kind, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch kind {
				case "unknown-length-large":
					w.(http.Flusher).Flush()
					io.WriteString(w, strings.Repeat("x", 33))
				case "truncated":
					w.Header().Set("Content-Length", "20")
					io.WriteString(w, "abc")
				case "http-error":
					http.Error(w, "not found", 404)
				}
			}))
			defer server.Close()
			s := &ReviewService{client: server.Client()}
			dir := t.TempDir()
			path, err := s.download(context.Background(), server.URL, dir, ".mp4", 32)
			if err == nil || path != "" {
				t.Fatal("invalid download accepted")
			}
			files, _ := filepath.Glob(filepath.Join(dir, "*"))
			if len(files) != 0 {
				t.Fatalf("partial files leaked: %v", files)
			}
		})
	}
}

func TestReviewEligibilityUsesRawZoneNamesAndEndTime(t *testing.T) {
	conf := config.New()
	conf.FrigateIncludeLabel = []string{"person", "car"}
	conf.FrigateIncludeSeverity = []string{"alert"}
	conf.FrigateIncludeZone = []string{"garage-alert-zone"}
	s := &ReviewService{conf: conf}
	r := testReview()
	now := time.Unix(1200, 0)
	if !s.eligible(r, now, 1050) {
		t.Fatal("episode crossing baseline or extra zone was discarded")
	}
	r.Severity = "detection"
	if s.eligible(r, now, 1) {
		t.Fatal("detection admitted")
	}
	r.Severity = "alert"
	r.Data.Objects = []string{"dog"}
	if s.eligible(r, now, 1) {
		t.Fatal("dog admitted")
	}
	r.Data.Objects = []string{"person"}
	r.Data.Zones = []string{"garage-zone"}
	if s.eligible(r, now, 1) {
		t.Fatal("outside alert zone admitted")
	}
	r.Data.Zones = []string{"garage-alert-zone"}
	r.EndTime = 1195
	if s.eligible(r, now, 1) {
		t.Fatal("clip requested before settling")
	}
	r.EndTime = 0
	r.StartTime = 1170
	if s.eligible(r, now, 1) {
		t.Fatal("ongoing episode sent too early")
	}
	r.StartTime = 1100
	if !s.eligible(r, now, 1) {
		t.Fatal("long ongoing alert never sent")
	}
}

func TestRedisCommitRetryDoesNotResendTelegram(t *testing.T) {
	sends := 0
	r := testReview()
	s, store := newTestService(t, func(w http.ResponseWriter, req *http.Request) {
		switch {
		case strings.HasSuffix(req.URL.Path, "getMe"):
			mockGetMe(w)
		case req.URL.Path == "/api/review":
			json.NewEncoder(w).Encode([]Review{r})
		case strings.HasSuffix(req.URL.Path, "clip.mp4"):
			io.WriteString(w, "clip")
		case strings.HasSuffix(req.URL.Path, "sendVideo"):
			sends++
			io.Copy(io.Discard, req.Body)
			io.WriteString(w, `{"ok":true,"result":{"message_id":44}}`)
		default:
			http.NotFound(w, req)
		}
	})
	store.failFinish = true
	if err := s.deliver(context.Background(), r, time.Unix(1200, 0)); err == nil {
		t.Fatal("expected failed state commit")
	}
	store.failFinish = false
	if err := s.Poll(context.Background(), time.Unix(1201, 0)); err != nil {
		t.Fatal(err)
	}
	if sends != 1 || store.sent[r.ID] != 44 {
		t.Fatalf("commit retry resent message: %d", sends)
	}
	store.failState = true
	if err := s.Poll(context.Background(), time.Unix(1202, 0)); err == nil {
		t.Fatal("Redis outage must fail closed")
	}
	if sends != 1 {
		t.Fatal("sent during Redis outage")
	}
}

func TestUnavailableTempDirectoryStillSendsTextAlert(t *testing.T) {
	sends := 0
	r := testReview()
	s, store := newTestService(t, func(w http.ResponseWriter, req *http.Request) {
		switch {
		case strings.HasSuffix(req.URL.Path, "getMe"):
			mockGetMe(w)
		case strings.HasSuffix(req.URL.Path, "sendMessage"):
			sends++
			io.Copy(io.Discard, req.Body)
			io.WriteString(w, `{"ok":true,"result":{"message_id":45}}`)
		default:
			t.Errorf("unexpected request with unavailable temp storage: %s", req.URL)
			http.NotFound(w, req)
		}
	})
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))
	if err := s.deliver(context.Background(), r, time.Unix(1200, 0)); err != nil {
		t.Fatal(err)
	}
	if sends != 1 || store.sent[r.ID] != 45 {
		t.Fatal("alert lost when temporary storage unavailable")
	}
}
