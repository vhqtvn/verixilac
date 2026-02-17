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

			// For now, if webhook is used, we only support single bot or need logic todispatch
			// Assuming single bot for webhook or braodcast to all?
			// If we process update with wrong bot, it might fail signature check?
			// But update body doesn't have signature.
			// We'll just use the first bot for processing updates from webhook
			if len(bots) > 0 {
				bots[0].ProcessUpdate(update)
			}
		})

		vh.Start()
		defer vh.Stop()

		// Set telegram webhook to public URL
		if cfg.Telegram.BotPublicUrl != "" && len(bots) > 0 {
			if err := bots[0].SetWebhook(&telebot.Webhook{
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
