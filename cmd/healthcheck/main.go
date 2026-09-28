// Command healthcheck verifies that the three dependency services started by
// docker-compose are all reachable.
//
// Usage: go run ./cmd/healthcheck
// Exit code is 0 if all checks pass, 1 if any fail.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"
	"github.com/segmentio/kafka-go"

	"tradeforge/internal/config"
)

type check struct {
	name string
	run  func(context.Context, config.Config) (string, error)
}

func main() {
	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	checks := []check{
		{"postgres", checkPostgres},
		{"redis", checkRedis},
		{"kafka", checkKafka},
	}

	failed := 0
	for _, c := range checks {
		detail, err := c.run(ctx, cfg)
		if err != nil {
			failed++
			fmt.Printf("  [FAIL] %-9s %v\n", c.name, err)
			continue
		}
		fmt.Printf("  [ OK ] %-9s %s\n", c.name, detail)
	}

	fmt.Println()
	if failed > 0 {
		fmt.Printf("%d/%d checks failed. Confirm `docker compose up -d` has been run and the containers are healthy.\n", failed, len(checks))
		os.Exit(1)
	}
	fmt.Printf("All %d dependency services are reachable.\n", len(checks))
}

func checkPostgres(ctx context.Context, cfg config.Config) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	conn, err := pgx.Connect(ctx, cfg.Postgres.DSN())
	if err != nil {
		return "", fmt.Errorf("connection failed (%s:%d): %w", cfg.Postgres.Host, cfg.Postgres.Port, err)
	}
	defer conn.Close(context.Background())

	var version string
	if err := conn.QueryRow(ctx, "select version()").Scan(&version); err != nil {
		return "", fmt.Errorf("query failed: %w", err)
	}
	return truncate(version, 60), nil
}

func checkRedis(ctx context.Context, cfg config.Config) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	rdb := redis.NewClient(&redis.Options{
		Addr:     cfg.Redis.Addr,
		Password: cfg.Redis.Password,
		DB:       cfg.Redis.DB,
	})
	defer rdb.Close()

	if err := rdb.Ping(ctx).Err(); err != nil {
		return "", fmt.Errorf("PING failed (%s): %w", cfg.Redis.Addr, err)
	}

	// Also verify the read/write path — a successful PING alone doesn't prove
	// it works as a signal cache.
	const probeKey = "tradeforge:healthcheck"
	if err := rdb.Set(ctx, probeKey, time.Now().Format(time.RFC3339), time.Minute).Err(); err != nil {
		return "", fmt.Errorf("write failed: %w", err)
	}
	if err := rdb.Del(ctx, probeKey).Err(); err != nil {
		return "", fmt.Errorf("delete failed: %w", err)
	}
	return fmt.Sprintf("%s read/write OK", cfg.Redis.Addr), nil
}

func checkKafka(ctx context.Context, cfg config.Config) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	broker := cfg.Kafka.Brokers[0]
	conn, err := (&kafka.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", broker)
	if err != nil {
		return "", fmt.Errorf("connection failed (%s): %w", broker, err)
	}
	defer conn.Close()

	brokers, err := conn.Brokers()
	if err != nil {
		return "", fmt.Errorf("failed to read cluster metadata: %w", err)
	}
	return fmt.Sprintf("%s, cluster has %d broker(s)", broker, len(brokers)), nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
