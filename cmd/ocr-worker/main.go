// Command ocr-worker runs one approved ocr capability over Worker Protocol v1.
package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/tileforge/octoconv-worker-ocr/internal/convert"
)

func main() {
	if err := refuseLegacyStores(); err != nil {
		log.Fatalf("ocr protocol: %v", err)
	}
	if err := setOCRThreadsFromEnv(); err != nil {
		log.Fatalf("ocr protocol: %v", err)
	}
	applyOCRLimits()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	dial, err := loadProtocolDial()
	if err != nil {
		log.Fatalf("ocr protocol: %v", err)
	}
	log.Printf("ocr protocol worker starting")
	if err := runProtocol(ctx, dial); err != nil {
		log.Fatalf("ocr protocol: %v", err)
	}
}

func refuseLegacyStores() error {
	if os.Getenv("DATABASE_URL") != "" || os.Getenv("REDIS_ADDR") != "" {
		return errors.New("DATABASE_URL and REDIS_ADDR are not used")
	}
	return nil
}

func setOCRThreadsFromEnv() error {
	raw := os.Getenv("OCR_THREADS")
	if raw == "" {
		return nil
	}
	n, err := strconv.Atoi(firstField(raw))
	if err != nil || n < 1 {
		return errors.New("OCR_THREADS must be a positive integer")
	}
	convert.SetOCRThreads(n)
	return nil
}

func applyOCRLimits() {
	convert.Configure(
		envInt("OCR_MAX_PAGES", convert.DefaultMaxPages),
		uint64(envInt64("OCR_MAX_IMAGE_PIXELS", convert.DefaultMaxImagePixels)),
	)
}

func ocrFreeSlots() (int, error) {
	raw := os.Getenv("OCR_WORKER_CONCURRENCY")
	if raw == "" {
		return 1, nil
	}
	n, err := strconv.Atoi(firstField(raw))
	if err != nil || n != 1 {
		return 0, errors.New("OCR_WORKER_CONCURRENCY must be 1")
	}
	return 1, nil
}

func envDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(firstField(v)); err == nil {
			return d
		}
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(firstField(v)); err == nil {
			return n
		}
	}
	return def
}

func envInt64(key string, def int64) int64 {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.ParseInt(firstField(v), 10, 64); err == nil {
			return n
		}
	}
	return def
}

func firstField(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == ' ' || s[i] == '\t' {
			return s[:i]
		}
	}
	return s
}
