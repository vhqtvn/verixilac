package telegram

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/rs/zerolog/log"
	"gopkg.in/tucnak/telebot.v2"

	"github.com/psucodervn/verixilac/internal/game"
	"github.com/psucodervn/verixilac/internal/stringer"
)

func (h *Handler) CmdAdmin(m *telebot.Message) {
	p := h.joinServer(m)
	if !p.IsAdmin() {
		h.sendMessage(m.Chat, "Bạn không có quyền admin")
		return
	}
	ss := strings.Split(strings.TrimSpace(m.Payload), " ")
	if len(ss) == 0 {
		return
	}

	cmd := ss[0]
	switch cmd {
	case "pause":
		h.doAdminPause(m)
	case "resume":
		h.doAdminResume(m)
		// case "deposit":
		// 	h.doDeposit(m, p, ss[1:])
	}
}

func (h *Handler) doAdminPause(m *telebot.Message) {
	if err := h.game.Pause(h.ctx(m)); err != nil {
		h.sendMessage(m.Chat, game.EscapeMarkdownV2(stringer.Capitalize(err.Error())))
		return
	}
	h.broadcast(h.game.Players(), "‼️ Sẽ được cập nhật, không thể tạo ván mới\\!", false)
}

func (h *Handler) doAdminResume(m *telebot.Message) {
	if err := h.game.Resume(h.ctx(m)); err != nil {
		h.sendMessage(m.Chat, game.EscapeMarkdownV2(stringer.Capitalize(err.Error())))
		return
	}
	h.broadcast(h.game.Players(), "✅ Server đã mở lại, chơi ngay\\!", false)
}

func (h *Handler) doDeposit(m *telebot.Message, operator *game.Player, ss []string) {
	if len(ss) != 2 {
		h.sendMessage(m.Chat, "Cú pháp: /deposit player\\_id amount")
		return
	}

	id := ss[0]
	amount, err := strconv.ParseInt(ss[1], 10, 64)
	if err != nil {
		h.sendMessage(m.Chat, "Cú pháp: /deposit player\\_id amount")
		return
	}

	p, err := h.game.Deposit(h.ctx(m), id, amount)
	if err != nil {
		h.sendMessage(m.Chat, game.EscapeMarkdownV2(stringer.Capitalize(err.Error())))
		return
	}

	log.Info().Str("operator", operator.Name()).
		Str("operator_id", operator.ID()).
		Str("recipient", p.Name()).
		Str("recipient_id", p.ID()).
		Int64("amount", amount).Msg("deposit")

	msg := fmt.Sprintf("💰%s đã bơm vào %d🌷\\.", game.EscapeMarkdownV2(p.Name()), amount)
	if amount < 0 {
		msg = fmt.Sprintf("💸 %s đã rút ra %d🌷\\.", game.EscapeMarkdownV2(p.Name()), -amount)
	}
	h.broadcast(h.game.Players(), msg, false)
}
