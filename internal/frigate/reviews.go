package frigate

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/oldtyt/frigate-telegram/internal/config"
	"github.com/oldtyt/frigate-telegram/internal/log"
	"github.com/oldtyt/frigate-telegram/internal/redis"
)

type Review struct {
	ID        string  `json:"id"`
	Camera    string  `json:"camera"`
	StartTime float64 `json:"start_time"`
	EndTime   float64 `json:"end_time"`
	Severity  string  `json:"severity"`
	Data      struct {
		Detections []string `json:"detections"`
		Objects    []string `json:"objects"`
		Zones      []string `json:"zones"`
	} `json:"data"`
}

type reviewState interface {
	Start(context.Context, int64) (int64, error)
	State(context.Context) (bool, bool, error)
	Claim(context.Context, string, string, time.Duration) (bool, error)
	Finish(context.Context, string, string, int, time.Duration) error
	Release(context.Context, string, string) error
}

type ReviewService struct {
	conf   *config.Config
	client *http.Client
	bot    *tgbotapi.BotAPI
	store  reviewState
	// Remember successful sends if Redis is briefly unavailable at commit time.
	// A process crash between Telegram acceptance and Redis commit is inherently
	// ambiguous: Telegram send methods do not provide idempotency keys.
	pending map[string]pendingReview
}

type pendingReview struct {
	token     string
	messageID int
}

func NewReviewService(conf *config.Config, bot *tgbotapi.BotAPI) (*ReviewService, error) {
	if conf.SleepTime < 1 || conf.ReviewLookbackSeconds < 60 || conf.ReviewSettleSeconds < 0 || conf.ReviewMaxWaitSeconds < 1 || conf.ClipMaxSeconds < 1 || conf.ClipMaxSeconds > 300 || conf.MediaMaxBytes < 1 || conf.MediaMaxBytes > 49000000 || conf.RedisTTL <= conf.ReviewLookbackSeconds {
		return nil, fmt.Errorf("invalid review settings: positive poll/max wait/clip/media limits required; Redis TTL must exceed lookback")
	}
	u, err := url.Parse(conf.FrigateURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("invalid FRIGATE_URL")
	}
	return &ReviewService{conf: conf, client: &http.Client{Timeout: 30 * time.Second}, bot: bot, store: redis.NewReviewStore(conf.TelegramChatID), pending: make(map[string]pendingReview)}, nil
}

func (s *ReviewService) Run(ctx context.Context) {
	log.Info.Println("Review notifications enabled: one message per camera episode")
	for {
		if err := s.Poll(ctx, time.Now()); err != nil && ctx.Err() == nil {
			log.Error.Printf("Review poll: %s", s.safeError(err))
		}
		timer := time.NewTimer(time.Duration(s.conf.SleepTime) * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (s *ReviewService) safeError(err error) string {
	text := err.Error()
	if s.bot != nil && s.bot.Token != "" {
		text = strings.ReplaceAll(text, s.bot.Token, "[REDACTED]")
	}
	return text
}

func (s *ReviewService) Poll(ctx context.Context, now time.Time) error {
	// Retry delivery-state commits before any further Telegram sends.
	for id, pending := range s.pending {
		if err := s.store.Finish(ctx, id, pending.token, pending.messageID, time.Duration(s.conf.RedisTTL)*time.Second); err != nil {
			return fmt.Errorf("save accepted notification %s: %w", id, err)
		}
		delete(s.pending, id)
	}
	paused, _, err := s.store.State(ctx)
	if err != nil || paused {
		return err
	}
	candidate := int64(s.conf.ReviewAfter)
	if candidate == 0 {
		candidate = now.Unix()
	}
	baseline, err := s.store.Start(ctx, candidate)
	if err != nil {
		return err
	}
	after := math.Max(float64(baseline), float64(now.Unix()-int64(s.conf.ReviewLookbackSeconds)))
	reviews, err := s.fetchReviews(ctx, after)
	if err != nil {
		return err
	}
	sort.Slice(reviews, func(i, j int) bool { return reviews[i].StartTime < reviews[j].StartTime })
	for _, review := range reviews {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !s.eligible(review, now, after) {
			continue
		}
		if err := s.deliver(ctx, review, now); err != nil {
			log.Error.Printf("Review %s: %s", review.ID, s.safeError(err))
			// A successful send with an uncommitted state must stop this poll.
			if len(s.pending) > 0 {
				return err
			}
		}
	}
	return nil
}

func (s *ReviewService) fetchReviews(ctx context.Context, after float64) ([]Review, error) {
	q := url.Values{"after": {strconv.FormatFloat(after, 'f', 6, 64)}, "reviewed": {"1"}}
	// No event-count limit: applying a limit before filters can silently hide alerts.
	if len(s.conf.FrigateIncludeSeverity) == 1 && s.conf.FrigateIncludeSeverity[0] != "All" {
		q.Set("severity", s.conf.FrigateIncludeSeverity[0])
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(s.conf.FrigateURL, "/")+"/api/review?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Frigate review HTTP %d", resp.StatusCode)
	}
	const maxJSON = 8 * 1024 * 1024
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxJSON+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxJSON {
		return nil, fmt.Errorf("review response too large")
	}
	var reviews []Review
	if err = json.Unmarshal(body, &reviews); err != nil {
		return nil, err
	}
	return reviews, nil
}

func intersects(values, filter []string) bool {
	for _, value := range values {
		for _, accepted := range filter {
			if value == strings.TrimSpace(accepted) {
				return true
			}
		}
	}
	return false
}

func included(values, filter []string) bool {
	return len(filter) == 1 && strings.TrimSpace(filter[0]) == "All" || intersects(values, filter)
}

func excluded(values, filter []string) bool {
	return !(len(filter) == 1 && strings.TrimSpace(filter[0]) == "None") && intersects(values, filter)
}

func (s *ReviewService) eligible(r Review, now time.Time, after float64) bool {
	if r.ID == "" || r.Camera == "" || r.StartTime <= 0 || math.IsNaN(r.StartTime) || math.IsInf(r.StartTime, 0) {
		return false
	}
	if r.EndTime != 0 {
		if r.EndTime < r.StartTime || r.EndTime <= after || float64(now.Unix())-r.EndTime < float64(s.conf.ReviewSettleSeconds) {
			return false
		}
	} else if float64(now.Unix())-r.StartTime < float64(s.conf.ReviewMaxWaitSeconds) {
		return false
	}
	return included([]string{r.Camera}, s.conf.FrigateIncludeCamera) && !excluded([]string{r.Camera}, s.conf.FrigateExcludeCamera) &&
		included(r.Data.Objects, s.conf.FrigateIncludeLabel) && !excluded(r.Data.Objects, s.conf.FrigateExcludeLabel) &&
		included(r.Data.Zones, s.conf.FrigateIncludeZone) && !excluded(r.Data.Zones, s.conf.FrigateExcludeZone) && included([]string{r.Severity}, s.conf.FrigateIncludeSeverity)
}

func (s *ReviewService) deliver(parent context.Context, r Review, now time.Time) error {
	ctx, cancel := context.WithTimeout(parent, 2*time.Minute)
	defer cancel()
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return err
	}
	token := hex.EncodeToString(bytes[:])
	ok, err := s.store.Claim(ctx, r.ID, token, 5*time.Minute)
	if err != nil || !ok {
		return err
	}
	// On failed sends retain the lease until expiry to avoid tight retry storms,
	// including ambiguous HTTP timeouts after Telegram may have accepted a send.
	paused, muted, err := s.store.State(ctx)
	if err != nil {
		return err
	}
	if paused {
		return s.store.Release(ctx, r.ID, token)
	}
	dir, dirErr := os.MkdirTemp("", "frigate-review-")
	if dirErr == nil {
		defer os.RemoveAll(dir)
	}
	caption := s.caption(r, now)
	var msg tgbotapi.Chattable
	video := ""
	err = dirErr
	if dirErr == nil {
		video, err = s.download(ctx, s.clipURL(r, now), dir, ".mp4", int64(s.conf.MediaMaxBytes))
	}
	if err == nil {
		v := tgbotapi.NewVideo(s.conf.TelegramChatID, tgbotapi.FilePath(video))
		v.Caption = caption + fmt.Sprintf("\nВидео: фрагмент до %d с.", s.conf.ClipMaxSeconds)
		v.SupportsStreaming = true
		v.DisableNotification = muted
		msg = v
	} else {
		log.Warn.Printf("Review %s video unavailable: %s", r.ID, s.safeError(err))
		if dirErr == nil && len(r.Data.Detections) > 0 {
			photoURL := strings.TrimRight(s.conf.FrigateURL, "/") + "/api/events/" + url.PathEscape(r.Data.Detections[0]) + "/thumbnail.jpg"
			photo, photoErr := s.download(ctx, photoURL, dir, ".jpg", 4*1024*1024)
			if photoErr == nil {
				p := tgbotapi.NewPhoto(s.conf.TelegramChatID, tgbotapi.FilePath(photo))
				p.Caption = caption + "\nВидео недоступно; запись можно открыть во Frigate."
				p.DisableNotification = muted
				msg = p
			}
		}
		if msg == nil {
			m := tgbotapi.NewMessage(s.conf.TelegramChatID, caption+"\nМедиа недоступно; запись можно открыть во Frigate.")
			m.DisableNotification = muted
			msg = m
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	// Re-check pause/mute after downloading; these commands must take effect promptly.
	paused, muted, err = s.store.State(ctx)
	if err != nil {
		return err
	}
	if paused {
		return s.store.Release(ctx, r.ID, token)
	}
	keyboard := tgbotapi.NewInlineKeyboardMarkup(tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonURL("Открыть Frigate", s.conf.FrigateExternalURL)))
	switch m := msg.(type) {
	case tgbotapi.VideoConfig:
		m.DisableNotification = muted
		m.ReplyMarkup = keyboard
		msg = m
	case tgbotapi.PhotoConfig:
		m.DisableNotification = muted
		m.ReplyMarkup = keyboard
		msg = m
	case tgbotapi.MessageConfig:
		m.DisableNotification = muted
		m.ReplyMarkup = keyboard
		msg = m
	}
	sent, err := s.bot.Send(msg)
	if err != nil {
		return fmt.Errorf("Telegram send: %w", err)
	}
	if sent.MessageID <= 0 {
		return fmt.Errorf("Telegram returned no message ID")
	}
	s.pending[r.ID] = pendingReview{token: token, messageID: sent.MessageID}
	// Commit even if shutdown arrived after Telegram accepted the message.
	commitCtx, commitCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer commitCancel()
	if err = s.store.Finish(commitCtx, r.ID, token, sent.MessageID, time.Duration(s.conf.RedisTTL)*time.Second); err != nil {
		return err
	}
	delete(s.pending, r.ID)
	log.Info.Printf("Review sent: id=%s camera=%s objects=%s message=%d", r.ID, r.Camera, strings.Join(r.Data.Objects, ","), sent.MessageID)
	return nil
}

func (s *ReviewService) caption(r Review, now time.Time) string {
	labels := make([]string, 0, len(r.Data.Objects))
	for _, label := range r.Data.Objects {
		switch label {
		case "person":
			label = "человек"
		case "car":
			label = "машина"
		}
		labels = append(labels, label)
	}
	text := fmt.Sprintf("Тревога · %s\n%s\n%s", r.Camera, strings.Join(labels, ", "), time.Unix(int64(r.StartTime), 0).In(now.Location()).Format("02.01.2006 15:04:05"))
	if len(r.Data.Zones) > 0 {
		text += "\nЗоны: " + strings.Join(r.Data.Zones, ", ")
	}
	if r.EndTime == 0 {
		text += "\nЭпизод продолжается."
	}
	runes := []rune(text)
	if len(runes) > 850 {
		text = string(runes[:850]) + "…"
	}
	return text
}

func (s *ReviewService) clipURL(r Review, now time.Time) string {
	start := math.Max(0, r.StartTime-3)
	end := r.EndTime + 3
	if r.EndTime == 0 {
		end = float64(now.Unix()) - 10
	}
	end = math.Min(end, start+float64(s.conf.ClipMaxSeconds))
	return fmt.Sprintf("%s/api/%s/start/%.6f/end/%.6f/clip.mp4", strings.TrimRight(s.conf.FrigateURL, "/"), url.PathEscape(r.Camera), start, end)
}

var errMediaTooLarge = errors.New("media exceeds configured byte limit")

// Download is bounded even when Content-Length is absent or dishonest. Partial
// files are always removed; callers own the successful file and its temp directory.
func (s *ReviewService) download(ctx context.Context, address, dir, suffix string, limit int64) (filename string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return "", err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("media HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > limit {
		return "", errMediaTooLarge
	}
	f, err := os.CreateTemp(dir, "media-*"+suffix)
	if err != nil {
		return "", err
	}
	path := f.Name()
	defer func() {
		f.Close()
		if err != nil {
			os.Remove(path)
		}
	}()
	n, err := io.Copy(f, io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return "", err
	}
	if n > limit {
		return "", errMediaTooLarge
	}
	if n == 0 {
		return "", errors.New("empty media")
	}
	if resp.ContentLength >= 0 && n != resp.ContentLength {
		return "", io.ErrUnexpectedEOF
	}
	if err = f.Close(); err != nil {
		return "", err
	}
	return path, nil
}
