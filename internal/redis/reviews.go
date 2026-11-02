package redis

import (
	"context"
	"fmt"
	"strconv"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// ReviewStore separates delivery history by destination chat. Locks are owned,
// so an expired worker cannot remove another worker's lease or mark its work done.
type ReviewStore struct {
	client *goredis.Client
	prefix string
}

func NewReviewStore(chatID int64) *ReviewStore {
	return &ReviewStore{client: rdb, prefix: fmt.Sprintf("frigate:reviews:v1:%d:", chatID)}
}

func (s *ReviewStore) Start(ctx context.Context, candidate int64) (int64, error) {
	key := s.prefix + "start"
	if err := s.client.SetNX(ctx, key, candidate, 0).Err(); err != nil {
		return 0, err
	}
	return s.client.Get(ctx, key).Int64()
}

func (s *ReviewStore) State(ctx context.Context) (paused, muted bool, err error) {
	pipe := s.client.Pipeline()
	stop := pipe.Exists(ctx, RedisKeyStateSendEvent)
	mute := pipe.Exists(ctx, RedisKeyStateMuteEvent)
	if _, err = pipe.Exec(ctx); err != nil {
		return false, false, err
	}
	return stop.Val() != 0, mute.Val() != 0, nil
}

var claimReview = goredis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 1 then return 0 end
if redis.call('SET', KEYS[2], ARGV[1], 'NX', 'PX', ARGV[2]) then return 1 end
return 0`)

func (s *ReviewStore) Claim(ctx context.Context, id, token string, ttl time.Duration) (bool, error) {
	n, err := claimReview.Run(ctx, s.client, []string{s.prefix + id + ":sent", s.prefix + id + ":lock"}, token, ttl.Milliseconds()).Int()
	return n == 1, err
}

var finishReview = goredis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 1 then return 1 end
local owner = redis.call('GET', KEYS[2])
if owner and owner ~= ARGV[1] then return 0 end
redis.call('SET', KEYS[1], ARGV[2], 'PX', ARGV[3])
redis.call('DEL', KEYS[2])
return 1`)

func (s *ReviewStore) Finish(ctx context.Context, id, token string, messageID int, ttl time.Duration) error {
	n, err := finishReview.Run(ctx, s.client, []string{s.prefix + id + ":sent", s.prefix + id + ":lock"}, token, strconv.Itoa(messageID), ttl.Milliseconds()).Int()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("review lease lost")
	}
	return nil
}

var releaseReview = goredis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then return redis.call('DEL', KEYS[1]) end
return 0`)

func (s *ReviewStore) Release(ctx context.Context, id, token string) error {
	return releaseReview.Run(ctx, s.client, []string{s.prefix + id + ":lock"}, token).Err()
}
