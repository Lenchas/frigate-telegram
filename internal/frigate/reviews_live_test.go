package frigate

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/oldtyt/frigate-telegram/internal/config"
)

// Explicitly opt in: this reads real Frigate media, never contacts Telegram,
// and does not write notification state to Redis.
func TestLiveFrigateBoundedClip(t *testing.T) {
	address := os.Getenv("TEST_FRIGATE_URL")
	if address == "" {
		t.Skip("set TEST_FRIGATE_URL for a read-only media smoke test")
	}
	conf := config.New()
	conf.FrigateURL = address
	conf.FrigateIncludeSeverity = []string{"alert"}
	s := &ReviewService{conf: conf, client: &http.Client{Timeout: 45 * time.Second}}
	reviews, err := s.fetchReviews(context.Background(), float64(time.Now().Unix()-86400))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range reviews {
		if r.EndTime == 0 {
			continue
		}
		path, err := s.download(context.Background(), s.clipURL(r, time.Now()), t.TempDir(), ".mp4", int64(conf.MediaMaxBytes))
		if err != nil {
			t.Fatal(err)
		}
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		header := make([]byte, 12)
		if _, err = file.Read(header); err != nil {
			t.Fatal(err)
		}
		if string(header[4:8]) != "ftyp" {
			t.Fatalf("unexpected media header: %q", header)
		}
		stat, _ := file.Stat()
		t.Logf("Downloaded bounded MP4: review=%s camera=%s bytes=%d", r.ID, r.Camera, stat.Size())
		return
	}
	t.Fatal("no completed alert review available for smoke test")
}
