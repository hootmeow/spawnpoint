package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/hootmeow/spawnpoint/internal/storage/postgres"
)

// version is set at build time via -ldflags "-X main.version=vX.Y.Z"; update.sh
// and setup.sh both pass it as `git describe --tags`. Defaults to "dev" for
// `go run`/unversioned builds.
var version = "dev"

func main() {
	databaseURL := flag.String("database-url", getenv("DATABASE_URL", ""), "PostgreSQL connection URL")
	refreshSummaries := flag.Bool("refresh-summaries", false, "refresh materialized summary views")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	if *databaseURL == "" {
		log.Fatal("DATABASE_URL or -database-url is required")
	}
	if !*refreshSummaries {
		log.Fatal("no action selected; use -refresh-summaries")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	store, err := postgres.Open(ctx, *databaseURL)
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()
	if err := store.RefreshSummaries(ctx); err != nil {
		log.Fatal(err)
	}
	log.Print("refreshed summary views")
}

func getenv(key string, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
