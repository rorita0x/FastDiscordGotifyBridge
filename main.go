// Command fastdiscordgotifybridge forwards messages from specific Discord
// channels to a Gotify server in real time, using a Discord user token.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"fastdiscordgotifybridge/internal/certs"
	"fastdiscordgotifybridge/internal/config"
	"fastdiscordgotifybridge/internal/discord"
	"fastdiscordgotifybridge/internal/gotify"
)

func main() {
	configFlag := flag.String("config", "config.toml", "path to config file (overridden by CONFIG_PATH)")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	if err := run(*configFlag, logger); err != nil {
		logger.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(configPath string, logger *slog.Logger) error {
	cfg, err := config.Load(config.ResolvePath(configPath))
	if err != nil {
		return err
	}

	pool, err := certs.Pool()
	if err != nil {
		return err
	}

	targets := cfg.Targets()
	logger.Info("loaded config", "channels", len(targets), "gotify", cfg.Gotify.URL)

	gc := gotify.New(cfg.Gotify.URL, cfg.Gotify.Token, pool)

	// Decouple the gateway read loop from Gotify HTTP latency with a buffered
	// queue and a single worker. The gateway handler must never block.
	queue := make(chan forward, 256)
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		for f := range queue {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			err := gc.Send(ctx, f.msg)
			cancel()
			if err != nil {
				logger.Error("gotify send failed", "channel", f.channel, "err", err)
			} else {
				logger.Info("forwarded", "channel", f.channel, "label", f.msg.Title)
			}
		}
	}()

	handler := func(m discord.Message) {
		t, ok := targets[m.ChannelID]
		if !ok {
			return
		}
		if cfg.Discord.IgnoreBots && m.AuthorBot {
			return
		}
		if strings.TrimSpace(m.Content) == "" && len(m.Attachments) == 0 {
			return // system message / embed-only with no text
		}

		gm := gotify.Message{
			Title:    buildTitle(t, m),
			Message:  buildBody(m),
			Priority: t.Priority,
		}
		select {
		case queue <- forward{channel: m.ChannelID, msg: gm}:
		default:
			logger.Warn("forward queue full; dropping message", "channel", m.ChannelID)
		}
	}

	gw := discord.New(discord.Options{
		Token:     cfg.Discord.UserToken,
		Handler:   handler,
		NotifyOwn: cfg.Discord.NotifyOwn,
		RootCAs:   pool,
		Logger:    logger,
	})

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	logger.Info("starting Discord -> Gotify bridge")
	err = gw.Run(ctx)

	// Drain the queue so in-flight notifications still go out on shutdown.
	close(queue)
	<-workerDone

	if err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	logger.Info("shutdown complete")
	return nil
}

type forward struct {
	channel string
	msg     gotify.Message
}

func buildTitle(t config.Target, m discord.Message) string {
	if t.Label != "" {
		return t.Label
	}
	return "Discord"
}

func buildBody(m discord.Message) string {
	var b strings.Builder
	b.WriteString(m.AuthorName)
	b.WriteString(": ")
	b.WriteString(m.Content)
	for _, url := range m.Attachments {
		b.WriteString("\n")
		b.WriteString(url)
	}
	b.WriteString("\n")
	b.WriteString(messageLink(m))
	return b.String()
}

// messageLink builds a Discord jump link to the original message. For direct
// messages GuildID is empty, in which case Discord uses the "@me" segment.
func messageLink(m discord.Message) string {
	guild := m.GuildID
	if guild == "" {
		guild = "@me"
	}
	return "https://discord.com/channels/" + guild + "/" + m.ChannelID + "/" + m.ID
}
