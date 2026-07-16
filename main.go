package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/lsongdev/miya-agents/logging"
	channelapp "github.com/lsongdev/miya-channels/app"
)

func main() {
	if err := logging.SetupFromDefaultConfig("miya-channels"); err != nil {
		log.Printf("[WARN] logging setup failed: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := channelapp.Run(ctx, channelapp.Options{}); err != nil {
		log.Fatal(err)
	}
}
