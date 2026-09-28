// Command healthcheck 验证 docker-compose 起的三个依赖服务都能连上。
//
// 用法：go run ./cmd/healthcheck
// 全部通过时退出码为 0，任意一项失败为 1。
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
		fmt.Printf("%d/%d 项检查失败。请确认 `docker compose up -d` 已执行且容器健康。\n", failed, len(checks))
		os.Exit(1)
	}
	fmt.Printf("全部 %d 项依赖服务连接正常。\n", len(checks))
}

func checkPostgres(ctx context.Context, cfg config.Config) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	conn, err := pgx.Connect(ctx, cfg.Postgres.DSN())
	if err != nil {
		return "", fmt.Errorf("连接失败 (%s:%d)：%w", cfg.Postgres.Host, cfg.Postgres.Port, err)
	}
	defer conn.Close(context.Background())

	var version string
	if err := conn.QueryRow(ctx, "select version()").Scan(&version); err != nil {
		return "", fmt.Errorf("查询失败：%w", err)
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
		return "", fmt.Errorf("PING 失败 (%s)：%w", cfg.Redis.Addr, err)
	}

	// 顺带验证读写通路，只 PING 通不代表能正常用作信号缓存。
	const probeKey = "tradeforge:healthcheck"
	if err := rdb.Set(ctx, probeKey, time.Now().Format(time.RFC3339), time.Minute).Err(); err != nil {
		return "", fmt.Errorf("写入失败：%w", err)
	}
	if err := rdb.Del(ctx, probeKey).Err(); err != nil {
		return "", fmt.Errorf("删除失败：%w", err)
	}
	return fmt.Sprintf("%s 读写正常", cfg.Redis.Addr), nil
}

func checkKafka(ctx context.Context, cfg config.Config) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	broker := cfg.Kafka.Brokers[0]
	conn, err := (&kafka.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", broker)
	if err != nil {
		return "", fmt.Errorf("连接失败 (%s)：%w", broker, err)
	}
	defer conn.Close()

	brokers, err := conn.Brokers()
	if err != nil {
		return "", fmt.Errorf("读取集群元数据失败：%w", err)
	}
	return fmt.Sprintf("%s，集群 %d 个 broker", broker, len(brokers)), nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
