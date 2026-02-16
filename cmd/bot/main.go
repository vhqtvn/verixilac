package bot

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"os/signal"
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

	var poller telebot.Poller
	if !useWebhook {
		poller = &telebot.LongPoller{Timeout: 10 * time.Second}
	}

	bot, err := telebot.NewBot(telebot.Settings{
		Token:  cfg.Telegram.BotToken,
		Poller: poller,
	})
	if err != nil {
		log.Fatal().Err(err).Msg("failed to connect to telegram bot")
	}

	manager := game.NewManager(cfg.MaxBet, cfg.MinDeal, cfg.Timeout)

	bh := telegram.NewHandler(manager, bot)

	if useWebhook {
		log.Info().
			Str("ingress_url", cfg.Telegram.BotIngressUrl).
			Str("public_url", cfg.Telegram.BotPublicUrl).
			Msg("starting in VH webhook mode")

		// Register handlers without starting the long-poller
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

			bot.ProcessUpdate(update)
		})

		vh.Start()
		defer vh.Stop()

		// Set telegram webhook to public URL
		if cfg.Telegram.BotPublicUrl != "" {
			if err := bot.SetWebhook(&telebot.Webhook{
				Listen: ":8443",
				Endpoint: &telebot.WebhookEndpoint{
					PublicURL: cfg.Telegram.BotPublicUrl,
				},
			}); err != nil {
				log.Error().Err(err).Msg("failed to set telegram webhook")
			} else {
				log.Info().Str("url", cfg.Telegram.BotPublicUrl).Msg("telegram webhook set")
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
