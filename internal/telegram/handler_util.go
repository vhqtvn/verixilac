package telegram

import (
	"context"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"golang.org/x/time/rate"
	"gopkg.in/tucnak/telebot.v2"

	"github.com/psucodervn/verixilac/internal/game"
)

var retryAfterRegex = regexp.MustCompile(`telegram: retry after (\d+) \(429\)`)

// --- Queue types ---

type botRequestType int

const (
	botRequestSend botRequestType = iota
	botRequestEdit
	botRequestEditMarkup
)

type botRequest struct {
	reqType     botRequestType
	chat        *telebot.Chat        // for Send
	message     *telebot.Message     // for Edit / EditMarkup
	what        interface{}          // message text or media
	options     *telebot.SendOptions // send/edit options
	markup      *telebot.ReplyMarkup // for EditMarkup only
	editKey     string               // dedup key for edits (empty = no dedup)
	fixedBotIdx int                  // -1 = any/mapped, >= 0 = forced bot
	result      chan botResponse     // nil = fire-and-forget
}

type botResponse struct {
	msg    *telebot.Message
	err    error
	botIdx int
}

const (
	sendQueueSize                   = 1024
	callbackQueueSize               = 1024
	rateLimitMessagePerChat         = 1007 * time.Millisecond
	rateLimitMessagePerSecondGlobal = 20

	messageTypeNormal = 0
	messageTypeLog    = 1
)

func (h *Handler) getPlayerMutex(id string) *sync.Mutex {
	v, _ := h.playerLocks.LoadOrStore(id, &sync.Mutex{})
	return v.(*sync.Mutex)
}

func (h *Handler) getChatWorker(chatID uint64) chan *botRequest {
	// fast path
	if ch, ok := h.chatWorkers.Load(chatID); ok {
		return ch.(chan *botRequest)
	}

	// create
	ch := make(chan *botRequest, 128)

	actual, loaded := h.chatWorkers.LoadOrStore(chatID, ch)
	if loaded {
		// someone else won the race
		close(ch)
		return actual.(chan *botRequest)
	}

	// start worker for this chat
	go h.runChatWorker(chatID, ch)
	return ch
}

func (h *Handler) getBot(chatID int64) *telebot.Bot {
	if val, ok := h.userBotMap.Load(chatID); ok {
		idx := val.(int)
		if idx >= 0 && idx < len(h.bots) {
			return h.bots[val.(int)]
		}
	}
	if len(h.bots) > 0 {
		return h.bots[0]
	}
	return nil
}

func (h *Handler) notifySwitchBots(chatID int64, currentBotIndex int) {
	if len(h.bots) <= 1 {
		return
	}

	var sb strings.Builder
	sb.WriteString("⚠️ Bot đang bị giới hạn tốc độ\\. Vui lòng chuyển sang các bot sau để tiếp tục:\n")

	count := 0
	for i, username := range h.botUsernames {
		if i == currentBotIndex {
			continue
		}
		sb.WriteString(fmt.Sprintf("\\- [%s](https://t.me/%s)\n", game.EscapeMarkdownV2(username), username))
		count++
	}

	if count == 0 {
		return
	}

	msg := sb.String()
	chat := &telebot.Chat{ID: chatID}

	// Try to send via other bots first (if user has started them, it will work)
	for i, bot := range h.bots {
		if i == currentBotIndex {
			continue
		}
		// Use a fire-and-forget approach running in a separate goroutine
		go func(b *telebot.Bot) {
			_, _ = b.Send(chat, msg, &telebot.SendOptions{ParseMode: telebot.ModeMarkdownV2})
		}(bot)
	}

	// Also try to send via current bot (might be delayed but better than nothing)
	go func() {
		if currentBotIndex >= 0 && currentBotIndex < len(h.bots) {
			bot := h.bots[currentBotIndex]
			_, _ = bot.Send(chat, msg, &telebot.SendOptions{ParseMode: telebot.ModeMarkdownV2})
		}
	}()
}

func (h *Handler) runChatWorker(chatID uint64, ch chan *botRequest) {
	limiter := rate.NewLimiter(rate.Every(rateLimitMessagePerChat), 3)

	for req := range ch {
		_ = limiter.Wait(context.Background())

		var (
			m   *telebot.Message
			err error
		)

		currentBotIndex := 0
		if val, ok := h.userBotMap.Load(int64(chatID)); ok {
			currentBotIndex = val.(int)
		}
		// Sanity check
		if currentBotIndex < 0 || currentBotIndex >= len(h.bots) {
			currentBotIndex = 0
		}

		// Create attemptOrder
		attemptOrder := make([]int, 0, len(h.bots))

		if req.fixedBotIdx >= 0 && req.fixedBotIdx < len(h.bots) {
			// If fixed bot is requested, ONLY try that bot
			currentBotIndex = req.fixedBotIdx
			attemptOrder = append(attemptOrder, currentBotIndex)
		} else {
			// Normal failover logic
			attemptOrder = append(attemptOrder, currentBotIndex)
			for i := 0; i < len(h.bots); i++ {
				if i != currentBotIndex {
					attemptOrder = append(attemptOrder, i)
				}
			}
		}

		success := false
		for _, botIdx := range attemptOrder {
			bot := h.bots[botIdx]
			if bot == nil {
				continue
			}

			// Inner retry loop for specific bot (e.g. temporary network flake)
			retryCount := 0
			var chatReq *telebot.Chat
			for {
				// Global rate limit per bot
				if botIdx >= 0 && botIdx < len(h.botLimiters) {
					_ = h.botLimiters[botIdx].Wait(context.Background())
				}

				switch req.reqType {
				case botRequestSend:
					chatReq = req.chat
					if req.options != nil {
						m, err = bot.Send(req.chat, req.what, req.options)
					} else {
						m, err = bot.Send(req.chat, req.what)
					}

				case botRequestEdit:
					chatReq = req.message.Chat
					if req.options != nil {
						m, err = bot.Edit(req.message, req.what, req.options)
					} else {
						m, err = bot.Edit(req.message, req.what)
					}

				case botRequestEditMarkup:
					chatReq = req.message.Chat
					m, err = bot.Edit(req.message, req.message.Text, &telebot.SendOptions{
						ReplyMarkup: req.markup,
					})
				}

				if err == nil {
					success = true
					break // break inner retry loop
				}

				// Check for 429 Retry After
				if matches := retryAfterRegex.FindStringSubmatch(err.Error()); len(matches) > 1 {
					seconds, _ := strconv.Atoi(matches[1])
					if seconds < 5 {
						log.Warn().Int("bot_idx", botIdx).Str("user", h.GetUsername(chatReq)).Int("seconds", seconds).Msg("telegram rate limit, sleeping")
						time.Sleep(time.Duration(seconds) * time.Second)
						continue
					} else {
						log.Warn().Int("bot_idx", botIdx).Str("user", h.GetUsername(chatReq)).Int("seconds", seconds).Msg("telegram rate limit, switch to next bot")
						h.notifySwitchBots(int64(chatID), botIdx)

						// If rate limited, we can try switching bot immediately in outer loop?
						// Yes, break inner loop and let outer loop try next bot.
						// But we should NOT increment retryCount here as it's a "soft" failure for this bot.
						break
					}
				}

				// Check for Forbidden/Unauthorized - these are permanent for this bot
				errMsg := err.Error()
				if reflect.TypeOf(err).String() == "*telebot.Error" {
					// Telebot errors might have Code/Description
				}
				// Simple string check is robust enough for standard telegram errors
				if isPermanentError(errMsg) {
					log.Warn().Int("bot_idx", botIdx).Str("user", h.GetUsername(chatReq)).Err(err).Msg("bot failed permanently, switching")
					break // break inner loop, try next bot
				}

				log.Err(err).Interface("what", req.what).Str("user", h.GetUsername(chatReq)).Int("type", int(req.reqType)).Msg("bot request failed")
				retryCount++
				if retryCount >= 3 {
					break // break inner loop, try next bot
				}
				time.Sleep(1 * time.Second)
			}

			if success {
				// Update mapping if we used a different bot
				if botIdx != currentBotIndex {
					h.userBotMap.Store(int64(chatID), botIdx)
				}
				break // break outer loop (bots)
			}
		}

		if req.result != nil {
			req.result <- botResponse{msg: m, err: err, botIdx: currentBotIndex}
		}
	}
}

func isPermanentError(msg string) bool {
	msg = reflect.ValueOf(msg).String() // simple string
	// "Forbidden: bot was blocked by the user"
	// "Forbidden: user is deactivated"
	// "Bad Request: chat not found"
	// "Unauthorized"
	return regexp.MustCompile(`(?i)(forbidden|unauthorized|chat not found|user not found)`).MatchString(msg)
}

// startQueue processes the send queue sequentially with rate limiting.
func (h *Handler) startQueue() {
	go func() {
		for {
			select {
			case req := <-h.sendQueue:
				// Edit deduplication: skip if a newer edit superseded this one
				if req.editKey != "" {
					latest, ok := h.editLatest.Load(req.editKey)
					if ok && latest.(*botRequest) != req {
						// A newer request exists for this key; discard this one
						if req.result != nil {
							req.result <- botResponse{}
						}
						continue
					}
					// Clean up the key after processing
					h.editLatest.Delete(req.editKey)
				}

				var chatId uint64
				switch req.reqType {
				case botRequestSend:
					chatId = uint64(req.chat.ID)
				case botRequestEdit:
					chatId = uint64(req.message.Chat.ID)
				case botRequestEditMarkup:
					chatId = uint64(req.message.Chat.ID)
				}

				h.getChatWorker(chatId) <- req

			case callback := <-h.callbackAckQueue:
				if bot := h.getBot(int64(callback.Sender.ID)); bot != nil {
					bot.Respond(callback, &telebot.CallbackResponse{})
				}
			}
		}
	}()
}

// --- Fire-and-forget API ---

// botSend enqueues a send request and returns immediately.
func (h *Handler) botSend(chat *telebot.Chat, what interface{}, options *telebot.SendOptions) {
	h.sendQueue <- &botRequest{
		reqType:     botRequestSend,
		chat:        chat,
		what:        what,
		options:     options,
		fixedBotIdx: -1,
	}
}

// botSendFixed enqueues a send request forced to a specific bot.
func (h *Handler) botSendFixed(chat *telebot.Chat, what interface{}, options *telebot.SendOptions, botIdx int) {
	h.sendQueue <- &botRequest{
		reqType:     botRequestSend,
		chat:        chat,
		what:        what,
		options:     options,
		fixedBotIdx: botIdx,
	}
}

// botEdit enqueues an edit request with deduplication key. Fire-and-forget.
// If editKey is non-empty, only the latest enqueued edit for that key will execute.
func (h *Handler) botEdit(editKey string, m *telebot.Message, what interface{}, options *telebot.SendOptions) {
	req := &botRequest{
		reqType:     botRequestEdit,
		message:     m,
		what:        what,
		options:     options,
		editKey:     editKey,
		fixedBotIdx: -1,
	}
	if editKey != "" {
		h.editLatest.Store(editKey, req)
	}
	h.sendQueue <- req
}

// botEditReplyMarkup enqueues an EditReplyMarkup request. Fire-and-forget.
func (h *Handler) botEditReplyMarkup(m *telebot.Message, markup *telebot.ReplyMarkup) {
	h.sendQueue <- &botRequest{
		reqType:     botRequestEditMarkup,
		message:     m,
		markup:      markup,
		fixedBotIdx: -1,
	}
}

// --- Sync API (blocks until result) ---

// botSendSync enqueues a send and waits for the result.
func (h *Handler) botSendSync(chat *telebot.Chat, what interface{}, options *telebot.SendOptions) (*telebot.Message, error, int) {
	ch := make(chan botResponse, 1)
	h.sendQueue <- &botRequest{
		reqType:     botRequestSend,
		chat:        chat,
		what:        what,
		options:     options,
		result:      ch,
		fixedBotIdx: -1,
	}
	res := <-ch
	return res.msg, res.err, res.botIdx
}

// botEditSync enqueues an edit and waits for the result.
func (h *Handler) botEditSync(editKey string, m *telebot.Message, what interface{}, options *telebot.SendOptions, fixedBotIdx int) (*telebot.Message, error, int) {
	ch := make(chan botResponse, 1)
	req := &botRequest{
		reqType:     botRequestEdit,
		message:     m,
		what:        what,
		options:     options,
		editKey:     editKey,
		result:      ch,
		fixedBotIdx: fixedBotIdx,
	}
	if editKey != "" {
		h.editLatest.Store(editKey, req)
	}
	h.sendQueue <- req
	res := <-ch
	return res.msg, res.err, res.botIdx
}

// --- High-level helpers (use the queue internally) ---

func (h *Handler) ctx(m *telebot.Message) context.Context {
	l := log.Logger.With().
		Int64("id", m.Chat.ID).
		Str("user", h.GetUsername(m.Chat)).
		Logger()
	return l.WithContext(context.Background())
}

func (h *Handler) sendMessage(chat *telebot.Chat, msg string, buttons ...InlineButton) {
	options := &telebot.SendOptions{ParseMode: telebot.ModeMarkdownV2}
	if len(buttons) > 0 {
		options.ReplyMarkup = &telebot.ReplyMarkup{
			InlineKeyboard: ToTelebotInlineButtons(buttons),
		}
	}
	h.botSend(chat, msg, options)
}

func (h *Handler) editMessage(m *telebot.Message, msg string, buttons ...InlineButton) {
	options := &telebot.SendOptions{ParseMode: telebot.ModeMarkdownV2}
	if len(buttons) > 0 {
		options.ReplyMarkup = &telebot.ReplyMarkup{
			InlineKeyboard: ToTelebotInlineButtons(buttons),
		}
	}
	editKey := fmt.Sprintf("edit:%d:%d", m.Chat.ID, m.ID)
	// Editing a specific message user interacted with doesn't necessarily change the "last game message" status
	// unless that message WAS the last game message.
	// But `editMessage` is usually for invalid input response or immediate feedback to user command.
	// Safe to ignore updating lastMessageType here?
	// Actually, if we edit a message, we might be overwriting a Log?
	// But `editMessage` edits `m`, which came from user updates.
	h.botEdit(editKey, m, msg, options)

}

func (h *Handler) broadcast(receivers interface{}, msg string, edit bool, buttons ...InlineButton) {
	var recvs []*game.Player
	switch v := receivers.(type) {
	case []*game.Player:
		recvs = v
	case *game.Player:
		recvs = append(recvs, v)
	case []*game.PlayerInGame:
		tmp := v
		for i := range tmp {
			recvs = append(recvs, tmp[i].Player)
		}
	case *game.PlayerInGame:
		recvs = append(recvs, v.Player)
	default:
		log.Error().Str("type", reflect.TypeOf(receivers).String()).Msg("invalid receivers type")
		return
	}

	wg := sync.WaitGroup{}
	for _, p := range recvs {
		wg.Add(1)
		p := p

		go func() {
			defer wg.Done()

			options := &telebot.SendOptions{
				ParseMode: telebot.ModeMarkdownV2,
				ReplyMarkup: &telebot.ReplyMarkup{
					InlineKeyboard: ToTelebotInlineButtons(buttons),
				},
			}
			pm, ok := h.gameMessages.Load(p.ID())
			if edit && ok && pm != nil {
				// If the last message was a LOG, we should NOT edit it with a normal message (State update),
				// because that would effectively delete the log history from the user's view (since logs are transient).
				// Instead, we should send a NEW message for the state update, which preserves the log history.
				lastType, _ := h.lastMessageType.Load(p.ID())
				isLog := lastType == messageTypeLog

				if edit && !isLog {
					// Update existing message
					h.lastMessageType.Store(p.ID(), messageTypeNormal)

					var msgToEdit *telebot.Message
					var botIdx = -1
					var lastText string
					var lastButtons []InlineButton

					switch v := pm.(type) {
					case SentMessage:
						msgToEdit = v.Message
						botIdx = v.BotIdx
						lastText = v.Text
						lastButtons = v.Buttons
					case *telebot.Message:
						msgToEdit = v
					}

					if msgToEdit != nil {
						if lastText == msg && ButtonsEqual(lastButtons, buttons) {
							return
						}
						editKey := fmt.Sprintf("game:%s", p.ID())
						m, err, idx := h.botEditSync(editKey, msgToEdit, msg, options, botIdx)
						if err != nil {
							log.Err(err).Str("receiver", p.Name()).Str("msg", msg).Msg("send message failed")
						} else if m != nil {
							h.gameMessages.Store(p.ID(), SentMessage{Message: m, BotIdx: idx, Text: msg, Buttons: buttons})
						}
					}
				} else {
					// Sending new message (either forced or because last message was a log)
					h.lastMessageType.Store(p.ID(), messageTypeNormal)

					m, err, idx := h.botSendSync(ToTelebotChat(p.ID()), msg, options)
					if err != nil {
						log.Err(err).Str("receiver", p.Name()).Str("msg", msg).Msg("send message failed")
					} else if m != nil {
						h.gameMessages.Store(p.ID(), SentMessage{Message: m, BotIdx: idx, Text: msg, Buttons: buttons})
					}
				}
			} else {

				// Sending new message
				h.lastMessageType.Store(p.ID(), messageTypeNormal)

				m, err, idx := h.botSendSync(ToTelebotChat(p.ID()), msg, options)
				if err != nil {
					log.Err(err).Str("receiver", p.Name()).Str("msg", msg).Msg("send message failed")
				} else if m != nil {
					h.gameMessages.Store(p.ID(), SentMessage{Message: m, BotIdx: idx, Text: msg, Buttons: buttons})
				}
			}
		}()
	}

	wg.Wait()
}

func (h *Handler) broadcastLog(receivers interface{}, msg string) {
	var recvs []*game.Player
	switch v := receivers.(type) {
	case []*game.Player:
		recvs = v
	case *game.Player:
		recvs = append(recvs, v)
	case []*game.PlayerInGame:
		tmp := v
		for i := range tmp {
			recvs = append(recvs, tmp[i].Player)
		}
	case *game.PlayerInGame:
		recvs = append(recvs, v.Player)
	default:
		log.Error().Str("type", reflect.TypeOf(receivers).String()).Msg("invalid receivers type")
		return
	}

	wg := sync.WaitGroup{}
	for _, p := range recvs {
		wg.Add(1)
		p := p

		go func() {
			defer wg.Done()

			lastType, _ := h.lastMessageType.Load(p.ID())
			pm, ok := h.gameMessages.Load(p.ID())

			if ok && pm != nil && lastType == messageTypeLog {
				// Append to previous message
				var prevMsg *telebot.Message
				var botIdx = -1

				switch v := pm.(type) {
				case SentMessage:
					prevMsg = v.Message
					botIdx = v.BotIdx
				case *telebot.Message:
					prevMsg = v
				}

				if prevMsg != nil {
					newText := prevMsg.Text + "\n" + msg

					// We don't store new message object because ID stays same, content changes.
					// But we need to update the Text in our stored copy if we want to append again?
					// botEditSync returns the edited message.

					editKey := fmt.Sprintf("game:%s", p.ID())
					// Use nil options to keep existing markup if any?
					// Usually logs don't have markup.
					m, err, idx := h.botEditSync(editKey, prevMsg, newText, nil, botIdx)
					if err != nil {
						log.Err(err).Str("receiver", p.Name()).Str("msg", msg).Msg("append log failed")
						// If edit fails (e.g. message too old), fall back to send?
						// For now just log error.
					} else if m != nil {
						h.gameMessages.Store(p.ID(), SentMessage{Message: m, BotIdx: idx})
					}
				}
			} else {
				// Send as new message
				h.lastMessageType.Store(p.ID(), messageTypeLog)

				options := &telebot.SendOptions{ParseMode: telebot.ModeMarkdownV2}
				m, err, idx := h.botSendSync(ToTelebotChat(p.ID()), msg, options)
				if err != nil {
					log.Err(err).Str("receiver", p.Name()).Str("msg", msg).Msg("send log failed")
				} else if m != nil {
					h.gameMessages.Store(p.ID(), SentMessage{Message: m, BotIdx: idx})
				}
			}
		}()
	}

	wg.Wait()

}

func (h *Handler) broadcastDeal(players []*game.Player, msg string, edit bool, version uint32, buttons ...InlineButton) {
	options := &telebot.SendOptions{
		ParseMode: telebot.ModeMarkdownV2,
		ReplyMarkup: &telebot.ReplyMarkup{
			InlineKeyboard: ToTelebotInlineButtons(buttons),
		},
	}

	wg := sync.WaitGroup{}
	for _, p := range players {
		wg.Add(1)
		p := p

		go func() {
			defer wg.Done()

			lock := h.getPlayerMutex(p.ID())
			lock.Lock()
			defer lock.Unlock()

			pm, ok := h.dealMessages.Load(p.ID())
			if edit && ok && pm != nil {
				var msgToEdit *telebot.Message
				var botIdx = -1
				var lastText string
				var lastButtons []InlineButton
				var lastVersion uint32

				switch v := pm.(type) {
				case SentMessage:
					msgToEdit = v.Message
					botIdx = v.BotIdx
					lastText = v.Text
					lastButtons = v.Buttons
					lastVersion = v.Version
				case *telebot.Message:
					msgToEdit = v
				}

				if msgToEdit != nil {
					if version > 0 && version < lastVersion {
						// Outdated update, skip
						return
					}

					if lastText == msg && ButtonsEqual(lastButtons, buttons) {
						// Content identical, just update version if needed
						if version > lastVersion {
							h.dealMessages.Store(p.ID(), SentMessage{Message: msgToEdit, BotIdx: botIdx, Text: lastText, Buttons: lastButtons, Version: version})
						}
						return
					}
					editKey := fmt.Sprintf("deal:%s", p.ID())
					m, err, idx := h.botEditSync(editKey, msgToEdit, msg, options, botIdx)
					if err != nil {
						log.Err(err).Str("receiver", p.Name()).Str("msg", msg).Msg("send message failed")
					} else if m != nil {
						h.dealMessages.Store(p.ID(), SentMessage{Message: m, BotIdx: idx, Text: msg, Buttons: buttons, Version: version})
					}
				}
			} else {
				// For new message, we don't check version against previous one strictly,
				// but we might want to respect if there was a logically newer message?
				// But "new message" usually means start of something or reset.
				m, err, idx := h.botSendSync(ToTelebotChat(p.ID()), msg, options)
				if err != nil {
					log.Err(err).Str("receiver", p.Name()).Str("msg", msg).Msg("send message failed")
				} else if m != nil {
					h.dealMessages.Store(p.ID(), SentMessage{Message: m, BotIdx: idx, Text: msg, Buttons: buttons, Version: version})
				}
			}
		}()
	}

	wg.Wait()
}

func (h *Handler) findPlayerInGame(m *telebot.Message, gameID string, playerID string) (*game.Game, *game.PlayerInGame) {
	g := h.game.FindGame(h.ctx(m), gameID)
	if g == nil {
		h.sendMessage(m.Chat, "Không tìm thấy ván "+gameID)
		return nil, nil
	}
	pg := g.FindPlayer(playerID)
	if pg == nil {
		h.sendMessage(m.Chat, "Không tìm thấy người chơi "+playerID)
		return g, nil
	}
	return g, pg
}

type Playable interface {
	ID() string
}

func (h *Handler) GetUsername(chat *telebot.Chat) string {
	if chat == nil {
		return "<nil>"
	}
	name := strings.TrimSpace(chat.FirstName + " " + chat.LastName)
	if len(name) == 0 {
		name = strings.TrimSpace(chat.Username)
	}

	// Fallback to DB if name is empty
	if len(name) == 0 {
		// Try to find player in DB
		// Note: we can't use h.ctx(m) here because h.ctx uses GetUsername!
		// So we use background context.
		p := h.game.FindPlayer(context.Background(), fmt.Sprintf("%v", chat.ID))
		if p != nil {
			name = p.Name()
		}
	}

	if len(name) == 0 {
		name = fmt.Sprintf("%v", chat.ID)
	}
	return name
}

func (h *Handler) getMediaUrl(bot *telebot.Bot, what interface{}) (string, error) {
	var fileID string
	switch v := what.(type) {
	case *telebot.Photo:
		fileID = v.FileID
	case *telebot.Video:
		fileID = v.FileID
	case *telebot.Animation:
		fileID = v.FileID
	case *telebot.Sticker:
		fileID = v.FileID
	default:
		return "", fmt.Errorf("unsupported media type: %T", what)
	}

	file, err := bot.FileByID(fileID)
	if err != nil {
		return "", err
	}
	return "https://api.telegram.org/file/bot" + bot.Token + "/" + file.FilePath, nil
}

func (h *Handler) mediaFromUrl(what interface{}, url string) interface{} {
	switch v := what.(type) {
	case *telebot.Photo:
		return &telebot.Photo{File: telebot.FromURL(url), Caption: v.Caption}
	case *telebot.Video:
		return &telebot.Video{File: telebot.FromURL(url), Caption: v.Caption}
	case *telebot.Animation:
		return &telebot.Animation{File: telebot.FromURL(url), Caption: v.Caption}
	case *telebot.Sticker:
		return &telebot.Sticker{File: telebot.FromURL(url)}
	default:
		return nil
	}
}
