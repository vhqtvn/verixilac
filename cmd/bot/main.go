package bot

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"gopkg.in/tucnak/telebot.v2"

	"github.com/psucodervn/verixilac/internal/config"
	"github.com/psucodervn/verixilac/internal/game"
	"github.com/psucodervn/verixilac/internal/telegram"
	"github.com/psucodervn/verixilac/internal/vhwebhook"
)

func Command() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bot",
		Short: "command description",
		Run:   run,
	}
	return cmd
}

func run(cmd *cobra.Command, args []string) {
	cfg := config.MustReadBotConfig()

	useWebhook := cfg.Telegram.BotIngressUrl != ""

	tokens := strings.Split(cfg.Telegram.BotToken, ",")
	var bots []*telebot.Bot
	for _, token := range tokens {
		var poller telebot.Poller
		if !useWebhook {
			poller = &telebot.LongPoller{Timeout: 10 * time.Second}
		} else {
			// If using webhook, we don't need poller, but we need the bot instance for API calls
			poller = &telebot.Webhook{} // Dummy or nil? Telebot requires poller?
			// checking telebot docs: NewBot requires Poller.
			// The original code used nil poller? No, original:
			/*
				var poller telebot.Poller
				if !useWebhook {
					poller = &telebot.LongPoller{Timeout: 10 * time.Second}
				}
			*/
			// So if useWebhook, poller is nil. Telebot NewBot might accept nil poller if we don't start it?
			// Actually NewBot returns error if Settings.Poller is nil unless we use Webhook?
			// Let's re-read original.
		}

		b, err := telebot.NewBot(telebot.Settings{
			Token:  token,
			Poller: poller,
		})
		if err != nil {
			log.Fatal().Err(err).Msg("failed to connect to telegram bot")
		}
		bots = append(bots, b)
	}

	manager := game.NewManager(cfg.MaxBet, cfg.MinDeal, cfg.Timeout)

	bh := telegram.NewHandler(manager, bots)

	// Map token -> bot for quick lookup if needed, or iterate
	// We'll use the token as path suffix
	botMap := make(map[string]*telebot.Bot)
	for _, b := range bots {
		botMap[b.Token] = b
	}

	if useWebhook {
		log.Info().
			Str("ingress_url", cfg.Telegram.BotIngressUrl).
			Str("public_url", cfg.Telegram.BotPublicUrl).
			Msg("starting in VH webhook mode")

		// Register handlers without starting the long-poller (already done in Setup)
		if err := bh.Setup(); err != nil {
			log.Fatal().Err(err).Msg("failed to setup bot handler")
		}

		vh := vhwebhook.New(
			cfg.Telegram.BotIngressUrl,
			cfg.Telegram.BotIngressAppName,
			cfg.Telegram.BotIngressToken,
			vhwebhook.Options{},
		)

		vh.OnWebhookRequest(func(req vhwebhook.WebhookRequest) {
			body, err := base64.StdEncoding.DecodeString(req.BodyB64)
			if err != nil {
				log.Error().Err(err).Msg("failed to decode webhook body")
				return
			}

			var update telebot.Update
			if err := json.Unmarshal(body, &update); err != nil {
				log.Error().Err(err).Msg("failed to parse telegram update")
				return
			}

			// Route based on path suffix (should contain token)
			// req.Path e.g. "/webhook/123456:ABC-DEF"
			// Or if we set PublicURL as base, Telegram appends nothing unless we specify.
			// Below we set Endpoint to PublicURL + "/" + token.
			// So path should end with token.

			var targetBot *telebot.Bot
			// Simple check: iterate bots and see if token is in path
			// Or check map against path suffix
			for token, b := range botMap {
				if strings.HasSuffix(req.Path, token) {
					targetBot = b
					break
				}
			}

			if targetBot == nil {
				// Fallback to first bot if path doesn't match (e.g. legacy setup)
				// Or log warning
				if len(bots) > 0 {
					targetBot = bots[0]
				} else {
					log.Warn().Str("path", req.Path).Msg("no matching bot for webhook path")
					return
				}
			}

			targetBot.ProcessUpdate(update)
		})

		vh.Start()
		defer vh.Stop()

		// Set telegram webhook for ALL bots to their unique URLs
		if cfg.Telegram.BotPublicUrl != "" {
			for _, b := range bots {
				// Ensure clean base URL
				baseURL := strings.TrimRight(cfg.Telegram.BotPublicUrl, "/")
				webhookURL := baseURL + "/" + b.Token

				if err := b.SetWebhook(&telebot.Webhook{
					Listen: ":8443", // internal listen port, unrelated to external URL?
					// Telebot Webhook struct uses Listen for starting server, but we use vhwebhook so we don't start server.
					// We just need to register the URL with Telegram.
					// Using &telebot.WebhookEndpoint is correct for SetWebhook call.
					Endpoint: &telebot.WebhookEndpoint{
						PublicURL: webhookURL,
					},
				}); err != nil {
					log.Error().Err(err).Str("bot", b.Me.Username).Msg("failed to set telegram webhook")
				} else {
					log.Info().Str("url", webhookURL).Str("bot", b.Me.Username).Msg("telegram webhook set")
				}
			}
		}

		// Block until signal
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		log.Info().Msg("shutting down")
	} else {
		if err := bh.Start(); err != nil {
			log.Fatal().Err(err).Msg("failed to start bot handler")
		}
	}
}
