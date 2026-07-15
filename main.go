package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	channelapp "github.com/lsongdev/miya-channels/app"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := channelapp.Run(ctx, channelapp.Options{}); err != nil {
		log.Fatal(err)
	}
}
