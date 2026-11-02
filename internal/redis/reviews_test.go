package redis

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

func TestReviewStoreAtomicClaimsAndLeaseOwnership(t *testing.T) {
	addr := os.Getenv("TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("set TEST_REDIS_ADDR to an isolated test Redis")
	}
	ctx := context.Background()
	client := goredis.NewClient(&goredis.Options{Addr: addr})
	defer client.Close()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatal(err)
	}
	store := &ReviewStore{client: client, prefix: fmt.Sprintf("test:reviews:%d:", time.Now().UnixNano())}
	defer func() {
		keys, _ := client.Keys(ctx, store.prefix+"*").Result()
		if len(keys) > 0 {
			client.Del(ctx, keys...)
		}
	}()
	start, err := store.Start(ctx, 100)
	if err != nil || start != 100 {
		t.Fatal(start, err)
	}
	start, err = store.Start(ctx, 200)
	if err != nil || start != 100 {
		t.Fatal("restart changed baseline", start, err)
	}
	var winners atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := store.Claim(ctx, "episode", "owner", time.Minute)
			if err != nil {
				t.Error(err)
			}
			if ok {
				winners.Add(1)
			}
		}()
	}
	wg.Wait()
	if winners.Load() != 1 {
		t.Fatalf("%d concurrent workers claimed one episode", winners.Load())
	}
	if err := store.Release(ctx, "episode", "stale"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := store.Claim(ctx, "episode", "other", time.Minute); ok {
		t.Fatal("stale owner released active lease")
	}
	if err := store.Finish(ctx, "episode", "stale", 7, time.Hour); err == nil {
		t.Fatal("stale owner finished active lease")
	}
	if err := store.Finish(ctx, "episode", "owner", 42, time.Hour); err != nil {
		t.Fatal(err)
	}
	if ok, _ := store.Claim(ctx, "episode", "restart", time.Minute); ok {
		t.Fatal("finished episode resent")
	}
	if ttl := client.TTL(ctx, store.prefix+"episode:sent").Val(); ttl < 59*time.Minute {
		t.Fatal("delivery history missing TTL", ttl)
	}
	// A delayed Redis commit may outlive its lease; acceptance must still be saved.
	if ok, err := store.Claim(ctx, "delayed", "owner", time.Minute); err != nil || !ok {
		t.Fatal(err)
	}
	client.Del(ctx, store.prefix+"delayed:lock")
	if err := store.Finish(ctx, "delayed", "owner", 43, time.Hour); err != nil {
		t.Fatal(err)
	}
	if ok, _ := store.Claim(ctx, "delayed", "restart", time.Minute); ok {
		t.Fatal("delayed successful send resent")
	}
	if RedisKeyStateMuteEvent == RedisKeyStateSendEvent {
		t.Fatal("mute and stop share a key")
	}
}
