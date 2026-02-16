package telegram

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"golang.org/x/time/rate"
	"gopkg.in/tucnak/telebot.v2"

	"github.com/psucodervn/verixilac/internal/game"
)

// --- Queue types ---

type botRequestType int

const (
	botRequestSend botRequestType = iota
	botRequestEdit
	botRequestEditMarkup
)

type botRequest struct {
	reqType botRequestType
	chat    *telebot.Chat        // for Send
	message *telebot.Message     // for Edit / EditMarkup
	text    string               // message text
	options *telebot.SendOptions // send/edit options
	markup  *telebot.ReplyMarkup // for EditMarkup only
	editKey string               // dedup key for edits (empty = no dedup)
	result  chan botResponse     // nil = fire-and-forget
}

type botResponse struct {
	msg *telebot.Message
	err error
}

const (
	sendQueueSize           = 1024
	callbackQueueSize       = 1024
	rateLimitMessagePerChat = 1007 * time.Millisecond
)

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

func (h *Handler) runChatWorker(chatID uint64, ch chan *botRequest) {
	limiter := rate.NewLimiter(rate.Every(rateLimitMessagePerChat), 3)

	for req := range ch {
		_ = limiter.Wait(context.Background())

		var (
			m   *telebot.Message
			err error
		)

		switch req.reqType {
		case botRequestSend:
			if req.options != nil {
				m, err = h.bot.Send(req.chat, req.text, req.options)
			} else {
				m, err = h.bot.Send(req.chat, req.text)
			}

		case botRequestEdit:
			if req.options != nil {
				m, err = h.bot.Edit(req.message, req.text, req.options)
			} else {
				m, err = h.bot.Edit(req.message, req.text)
			}

		case botRequestEditMarkup:
			m, err = h.bot.Edit(req.message, req.message.Text, &telebot.SendOptions{
				ReplyMarkup: req.markup,
			})
		}

		if err != nil {
			log.Err(err).Str("text", req.text).Int("type", int(req.reqType)).Msg("bot request failed")
		}

		if req.result != nil {
			req.result <- botResponse{msg: m, err: err}
		}
	}
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
				h.bot.Respond(callback, &telebot.CallbackResponse{})
			}
		}
	}()
}

// --- Fire-and-forget API ---

// botSend enqueues a send request and returns immediately.
func (h *Handler) botSend(chat *telebot.Chat, text string, options *telebot.SendOptions) {
	h.sendQueue <- &botRequest{
		reqType: botRequestSend,
		chat:    chat,
		text:    text,
		options: options,
	}
}

// botEdit enqueues an edit request with deduplication key. Fire-and-forget.
// If editKey is non-empty, only the latest enqueued edit for that key will execute.
func (h *Handler) botEdit(editKey string, m *telebot.Message, text string, options *telebot.SendOptions) {
	req := &botRequest{
		reqType: botRequestEdit,
		message: m,
		text:    text,
		options: options,
		editKey: editKey,
	}
	if editKey != "" {
		h.editLatest.Store(editKey, req)
	}
	h.sendQueue <- req
}

// botEditReplyMarkup enqueues an EditReplyMarkup request. Fire-and-forget.
func (h *Handler) botEditReplyMarkup(m *telebot.Message, markup *telebot.ReplyMarkup) {
	h.sendQueue <- &botRequest{
		reqType: botRequestEditMarkup,
		message: m,
		markup:  markup,
	}
}

// --- Sync API (blocks until result) ---

// botSendSync enqueues a send and waits for the result.
func (h *Handler) botSendSync(chat *telebot.Chat, text string, options *telebot.SendOptions) (*telebot.Message, error) {
	ch := make(chan botResponse, 1)
	h.sendQueue <- &botRequest{
		reqType: botRequestSend,
		chat:    chat,
		text:    text,
		options: options,
		result:  ch,
	}
	res := <-ch
	return res.msg, res.err
}

// botEditSync enqueues an edit and waits for the result.
func (h *Handler) botEditSync(editKey string, m *telebot.Message, text string, options *telebot.SendOptions) (*telebot.Message, error) {
	ch := make(chan botResponse, 1)
	req := &botRequest{
		reqType: botRequestEdit,
		message: m,
		text:    text,
		options: options,
		editKey: editKey,
		result:  ch,
	}
	if editKey != "" {
		h.editLatest.Store(editKey, req)
	}
	h.sendQueue <- req
	res := <-ch
	return res.msg, res.err
}

// --- High-level helpers (use the queue internally) ---

func (h *Handler) ctx(m *telebot.Message) context.Context {
	l := log.Logger.With().
		Int64("id", m.Chat.ID).
		Str("user", GetUsername(m.Chat)).
		Logger()
	return l.WithContext(context.Background())
}

func (h *Handler) sendMessage(chat *telebot.Chat, msg string, buttons ...InlineButton) {
	options := &telebot.SendOptions{}
	if len(buttons) > 0 {
		options.ReplyMarkup = &telebot.ReplyMarkup{
			InlineKeyboard: ToTelebotInlineButtons(buttons),
		}
	}
	h.botSend(chat, msg, options)
}

func (h *Handler) editMessage(m *telebot.Message, msg string, buttons ...InlineButton) {
	options := &telebot.SendOptions{}
	if len(buttons) > 0 {
		options.ReplyMarkup = &telebot.ReplyMarkup{
			InlineKeyboard: ToTelebotInlineButtons(buttons),
		}
	}
	editKey := fmt.Sprintf("edit:%d:%d", m.Chat.ID, m.ID)
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
				ReplyMarkup: &telebot.ReplyMarkup{
					InlineKeyboard: ToTelebotInlineButtons(buttons),
				},
			}
			pm, ok := h.gameMessages.Load(p.ID())
			if edit && ok && pm != nil {
				editKey := fmt.Sprintf("game:%s", p.ID())
				m, err := h.botEditSync(editKey, pm.(*telebot.Message), msg, options)
				if err != nil {
					log.Err(err).Str("receiver", p.Name()).Str("msg", msg).Msg("send message failed")
				} else if m != nil {
					h.gameMessages.Store(p.ID(), m)
				}
			} else {
				m, err := h.botSendSync(ToTelebotChat(p.ID()), msg, options)
				if err != nil {
					log.Err(err).Str("receiver", p.Name()).Str("msg", msg).Msg("send message failed")
				} else if m != nil {
					h.gameMessages.Store(p.ID(), m)
				}
			}
		}()
	}

	wg.Wait()
}

func (h *Handler) broadcastDeal(players []*game.Player, msg string, edit bool, buttons ...InlineButton) {
	options := &telebot.SendOptions{
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

			pm, ok := h.dealMessages.Load(p.ID())
			if edit && ok && pm != nil {
				editKey := fmt.Sprintf("deal:%s", p.ID())
				m, err := h.botEditSync(editKey, pm.(*telebot.Message), msg, options)
				if err != nil {
					log.Err(err).Str("receiver", p.Name()).Str("msg", msg).Msg("send message failed")
				} else if m != nil {
					h.dealMessages.Store(p.ID(), m)
				}
			} else {
				m, err := h.botSendSync(ToTelebotChat(p.ID()), msg, options)
				if err != nil {
					log.Err(err).Str("receiver", p.Name()).Str("msg", msg).Msg("send message failed")
				} else if m != nil {
					h.dealMessages.Store(p.ID(), m)
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
