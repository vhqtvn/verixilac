package telegram

import (
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/rs/zerolog/log"
	"github.com/spf13/cast"
	"gopkg.in/tucnak/telebot.v2"

	"golang.org/x/time/rate"

	"github.com/psucodervn/verixilac/internal/game"
	"github.com/psucodervn/verixilac/internal/stringer"
)

type Handler struct {
	game *game.Manager
	bots []*telebot.Bot

	userBotMap   sync.Map // int64 -> int (bot index)
	botUsernames []string // cached usernames for notification
	botLimiters  []*rate.Limiter

	gameMessages    sync.Map
	dealMessages    sync.Map
	lastMessageType sync.Map

	chatWorkers      sync.Map
	sendQueue        chan *botRequest
	callbackAckQueue chan *telebot.Callback

	editLatest sync.Map // editKey -> *botRequest for dedup

	mu sync.RWMutex
}

type SentMessage struct {
	Message *telebot.Message
	BotIdx  int
	Text    string
	Buttons []InlineButton
}

func NewHandler(manager *game.Manager, bots []*telebot.Bot) *Handler {
	h := &Handler{
		game:             manager,
		bots:             bots,
		sendQueue:        make(chan *botRequest, sendQueueSize),
		callbackAckQueue: make(chan *telebot.Callback, callbackQueueSize),
	}

	h.botUsernames = make([]string, len(bots))
	h.botLimiters = make([]*rate.Limiter, len(bots))
	for i, b := range bots {
		h.botUsernames[i] = b.Me.Username
		h.botLimiters[i] = rate.NewLimiter(rate.Limit(rateLimitMessagePerSecondGlobal), 10)
	}

	h.startQueue()
	return h
}

func (h *Handler) onCallback(q *telebot.Callback) {
	h.callbackAckQueue <- q

	ar := strings.SplitN(q.Data, " ", 2)
	if len(ar) > 1 {
		q.Message.Payload = ar[1]
	}
	switch ar[0] {
	case "/join":
		h.doJoinRoom(q.Message, true)
	case "/bet":
		h.doBet(q.Message, true)
	case "/deal":
		// dealer deal cards
		h.doDeal(q.Message, true)
	case "/cancel":
		// dealer cancel game
		h.doCancel(q.Message, true)
	case "/hit":
		h.doHit(q.Message, true)
	case "/stand":
		h.doStand(q.Message, true)
	case "/endgame":
		h.doEndGame(q.Message, true)
	case "/compare":
		h.doCompare(q.Message, true)
	case "/newgame":
		h.doNewGame(q.Message, true)
	default:
		log.Warn().Str("cmd", ar[0]).Msg("unknown query command")
	}
}

func (h *Handler) doBet(m *telebot.Message, onQuery bool) {
	ar := strings.Split(m.Payload, " ")
	if len(ar) != 2 {
		h.sendMessage(m.Chat, "Sai cú pháp")
		return
	}

	p := h.joinServer(m)
	ctx := h.ctx(m)
	gameID := strings.TrimSpace(ar[0])
	g := h.game.FindGame(ctx, gameID)
	if g == nil {
		h.sendMessage(m.Chat, "❌ Không có thông tin ván "+game.EscapeMarkdownV2(gameID))
		return
	}

	amount := cast.ToUint64(ar[1])
	if err := h.game.PlayerBet(ctx, g, p, amount); err != nil {
		h.sendMessage(m.Chat, "❌ "+game.EscapeMarkdownV2(stringer.Capitalize(err.Error())))
		return
	}
}

func (h *Handler) doDeal(m *telebot.Message, onQuery bool) {
	ctx := h.ctx(m)
	gameID := strings.TrimSpace(m.Payload)
	g := h.game.FindGame(ctx, gameID)
	if g == nil {
		h.sendMessage(m.Chat, "❌ Không tìm thấy ván "+game.EscapeMarkdownV2(gameID))
		return
	}

	if err := h.game.Deal(ctx, g); err != nil {
		h.sendMessage(m.Chat, game.EscapeMarkdownV2(stringer.Capitalize(err.Error())))
		return
	}

	h.broadcastDeal(g.Room().Players(), "🔒 *Chốt deal*:\n\n"+g.PreparingBoardMarkdownV2(), true)

	// send cards
	for _, pg := range g.Players() {
		if !pg.IsDone() {
			h.sendMessage(ToTelebotChat(pg.ID()), "Bài của bạn: "+pg.Cards().String(false))
		}
	}
	h.sendMessage(ToTelebotChat(g.Dealer().ID()), "Bài của bạn: "+g.Dealer().Cards().String(false, true))

	// start game
	if err := h.game.Start(ctx, g); err != nil {
		h.sendMessage(m.Chat, game.EscapeMarkdownV2(stringer.Capitalize(err.Error())))
		return
	}

	if !g.Finished() {
		for _, pg := range g.Players() {
			if !pg.IsDone() {
				continue
			}
			msg := fmt.Sprintf("🃏 Bài của %s: %s\n%s đã thắng %s🌷 🏆",
				game.EscapeMarkdownV2(pg.IconName()), pg.Cards().String(false, false),
				game.EscapeMarkdownV2(pg.IconName()), game.EscapeMarkdownV2(fmt.Sprintf("%d", pg.Reward())))
			h.broadcast(g.AllPlayers(), msg, false)
		}
	}

	// FIXME: fake
	if len(os.Getenv("TEST_ACCOUNT")) > 0 {
		m1 := &telebot.Message{ID: 123, Payload: g.ID(), Chat: &telebot.Chat{ID: 123, Username: "Test 1"}}
		for {
			ok := h.doStand(m1, true)
			if ok {
				break
			}
			if ok = h.doHit(m1, true); !ok {
				break
			}
		}

		m2 := &telebot.Message{ID: 456, Payload: g.ID(), Chat: &telebot.Chat{ID: 456, Username: "Test 2"}}
		for {
			ok := h.doStand(m2, true)
			if ok {
				break
			}
			if ok = h.doHit(m2, true); !ok {
				break
			}
		}
	}
}

func (h *Handler) doCancel(m *telebot.Message, onQuery bool) {
	p := h.joinServer(m)
	gameID := strings.TrimSpace(m.Payload)
	ctx := h.ctx(m)
	g, pg := h.findPlayerInGame(m, gameID, p.ID())
	if g == nil || pg == nil {
		return
	}
	if !pg.IsDealer() {
		h.sendMessage(m.Chat, "❌ Bạn không phải nhà cái")
		return
	}
	if err := h.game.CancelGame(ctx, g); err != nil {
		h.sendMessage(m.Chat, game.EscapeMarkdownV2(stringer.Capitalize(err.Error())))
		return
	}
	h.broadcast(g.Room().Players(), "⛔ "+game.EscapeMarkdownV2(pg.IconName())+" đã huỷ ván này", true, InlineButton{
		Text: "Tạo ván mới", Data: "/newgame",
	})
}

func (h *Handler) onNewRoom(r *game.Room, creator *game.Player) {
	// send to creator
	h.sendMessage(ToTelebotChat(creator.ID()), "✅ Bạn đã tạo phòng "+game.EscapeMarkdownV2(r.ID()), MakeNewlyCreatedRoomButtons(r)...)

	// send to other players in bot
	players := FilterPlayers(h.game.Players(), creator.ID())
	msg := "🏠 " + game.EscapeMarkdownV2(creator.IconName()) + " đã tạo phòng " + game.EscapeMarkdownV2(r.ID())
	buttons := []InlineButton{{Text: "Vào phòng", Data: "/join " + r.ID()}}
	h.broadcast(players, msg, false, buttons...)
}

func newGameMsgMarkdownV2(g *game.Game) string {
	return "📢 Bắt đầu ván mới, hãy tham gia ngay\\!\n\n" + g.PreparingBoardMarkdownV2()
}

func (h *Handler) onNewGame(r *game.Room, g *game.Game) {
	msg := newGameMsgMarkdownV2(g)

	// send to dealer
	d := g.Dealer()
	h.broadcastDeal([]*game.Player{d.Player}, msg, false, MakeDealerPrepareButtons(g)...)

	// send to members
	go func() {
		players := FilterPlayers(r.Players(), d.ID())
		h.broadcastDeal(players, msg, false, MakeBetButtons(g)...)
	}()
}

func (h *Handler) onPlayerJoinRoom(r *game.Room, p *game.Player) {
	players := FilterPlayers(r.Players(), p.ID())
	h.broadcast(players, "👋 "+game.EscapeMarkdownV2(p.IconName())+" vừa vào phòng "+game.EscapeMarkdownV2(r.ID()), false)
}

func (h *Handler) onPlayerBet(g *game.Game, p *game.PlayerInGame) {
	if p != nil {
		// Update the acting player immediately
		msg := newGameMsgMarkdownV2(g)
		h.broadcastDeal([]*game.Player{p.Player}, msg, true, MakeBetButtons(g)...)
	}

	go func() {
		msg := newGameMsgMarkdownV2(g)
		dealer := g.Dealer()
		h.broadcastDeal([]*game.Player{dealer.Player}, msg, true, MakeDealerPrepareButtons(g)...)

		r := g.Room()
		exclude := []string{dealer.ID()}
		if p != nil {
			exclude = append(exclude, p.ID())
		}
		players := FilterPlayers(r.Players(), exclude...)
		h.broadcastDeal(players, msg, true, MakeBetButtons(g)...)
	}()
}

func (h *Handler) doJoinRoom(m *telebot.Message, onQuery bool) {
	p := h.joinServer(m)
	roomID := strings.TrimSpace(m.Payload)
	r := h.game.FindRoom(h.ctx(m), roomID)
	if r == nil {
		h.sendMessage(m.Chat, "❌ "+game.EscapeMarkdownV2(stringer.Capitalize("Không tìm thấy phòng "+roomID)))
		return
	}

	if err := h.game.JoinRoom(h.ctx(m), p, r); err != nil {
		if err == game.ErrPlayerAlreadyInRoom {
			err = game.ErrYouAlreadyInRoom
		}
		h.sendMessage(m.Chat, game.EscapeMarkdownV2(stringer.Capitalize(err.Error())))
		return
	}
	if onQuery {
		h.editMessage(m, "Bạn đã vào phòng "+game.EscapeMarkdownV2(roomID))
	} else {
		h.sendMessage(m.Chat, "✅ Bạn đã vào phòng "+game.EscapeMarkdownV2(roomID))
	}
}

func (h *Handler) doEndGame(m *telebot.Message, onQuery bool) bool {
	p := h.joinServer(m)
	gameID := strings.TrimSpace(m.Payload)
	if len(gameID) == 0 {
		if p.CurrentGame() == nil {
			h.sendMessage(m.Chat, "❌ Bạn chưa vào ván")
			return false
		}
		gameID = p.CurrentGame().ID()
	}

	g, pg := h.findPlayerInGame(m, gameID, p.ID())
	if g == nil || pg == nil {
		return false
	}
	if !pg.IsDealer() {
		h.sendMessage(m.Chat, "Bạn không phải nhà cái")
		return false
	}

	if err := h.game.FinishGame(h.ctx(m), g, false); err != nil {
		h.sendMessage(m.Chat, game.EscapeMarkdownV2(stringer.Capitalize(err.Error())))
		return false
	}
	if onQuery {
		h.botEditReplyMarkup(m, nil)
	}
	return true
}

func (h *Handler) doStand(m *telebot.Message, onQuery bool) bool {
	p := h.joinServer(m)
	gameID := strings.TrimSpace(m.Payload)
	g, pg := h.findPlayerInGame(m, gameID, p.ID())
	if g == nil || pg == nil {
		return false
	}

	if err := h.game.PlayerStand(h.ctx(m), g, pg); err != nil {
		h.sendMessage(m.Chat, game.EscapeMarkdownV2(stringer.Capitalize(err.Error())))
		return false
	}
	if onQuery {
		h.botEditReplyMarkup(m, nil)
	}
	return true
}

func (h *Handler) doHit(m *telebot.Message, onQuery bool) bool {
	p := h.joinServer(m)
	force := false
	ar := strings.Split(strings.TrimSpace(m.Payload), " ")
	if len(ar) >= 2 {
		force = true
	}
	gameID := ar[0]
	g, pg := h.findPlayerInGame(m, gameID, p.ID())
	if g == nil || pg == nil {
		return false
	}

	if !force && pg.CanHit() && pg.Cards().Value() >= 18 {
		h.editMessage(m, "Bài của bạn: "+pg.Cards().String(false, pg.IsDealer())+"\nBạn chắc chắn muốn rút thêm?", MakePlayerButton(g, pg, true)...)
		return false
	}

	if err := h.game.PlayerHit(h.ctx(m), g, pg); err != nil {
		h.sendMessage(m.Chat, game.EscapeMarkdownV2(stringer.Capitalize(err.Error())))
		return false
	}
	return true
}

// joinServer check and register user
func (h *Handler) joinServer(m *telebot.Message) *game.Player {
	id := cast.ToString(m.Chat.ID)
	name := h.GetUsername(m.Chat)
	return h.game.PlayerRegister(h.ctx(m), id, name)
}

func (h *Handler) onPlayerStand(g *game.Game, pg *game.PlayerInGame) {
	// h.broadcast(g.Dealer(), pg.Name()+" đã úp bài", false)
	// h.broadcast(g.AllPlayers(), pg.Name()+" đã úp bài", false)
}

func (h *Handler) onPlayerHit(g *game.Game, pg *game.PlayerInGame) {
	if pg.IsDealer() {
		msg := h.getDealerDashboard(g, pg)
		buttons := MakeDealerDashboardButtons(g, pg)
		h.broadcast(pg, msg, true, buttons...)

		go func() {
			players := FilterInGamePlayers(g.AllPlayers(), pg.ID())
			h.broadcastLog(players, game.EscapeMarkdownV2(pg.IconName())+" vừa rút thêm 1 lá")
		}()
		return
	}

	go func() {
		players := FilterInGamePlayers(g.AllPlayers(), pg.ID())
		h.broadcastLog(players, game.EscapeMarkdownV2(pg.IconName())+" vừa rút thêm 1 lá")
	}()

	h.broadcast(pg, "Bài của bạn: "+pg.Cards().String(false, pg.IsDealer()), true, MakePlayerButton(g, pg, false)...)
}

func (h *Handler) doCompare(m *telebot.Message, onQuery bool) {
	ar := strings.Split(m.Payload, " ")
	if len(ar) != 2 {
		h.sendMessage(m.Chat, "Sai cú pháp")
		return
	}
	p := h.joinServer(m)
	g, dealer := h.findPlayerInGame(m, ar[0], p.ID())
	if g == nil || dealer == nil {
		return
	}
	if !dealer.IsDealer() {
		h.sendMessage(m.Chat, "Bạn không phải nhà cái")
		return
	}
	if !dealer.CanStand() {
		h.sendMessage(m.Chat, "Bạn chưa đủ tẩy")
		return
	}
	to := g.FindPlayer(ar[1])
	if to == nil {
		h.sendMessage(m.Chat, "Sai thông tin")
		return
	}

	reward, err := g.Done(to, false)
	if err != nil {
		h.sendMessage(m.Chat, game.EscapeMarkdownV2(stringer.Capitalize(err.Error())))
		return
	}
	if h.game.CheckIfFinish(h.ctx(m), g) {
		return
	}

	msgDealer := fmt.Sprintf("Bài của %s: %s",
		game.EscapeMarkdownV2(to.IconName()), to.Cards().String(false, false),
	)

	var msgPlayer string
	if reward < 0 {
		msgDealer += fmt.Sprintf("\n%s thắng và được cộng %d🌷", game.EscapeMarkdownV2(to.IconName()), -reward)
		msgPlayer = fmt.Sprintf("🤑 Cái lật bài bạn và thua\\. Bạn được cộng %d🌷", -reward)
	} else if reward > 0 {
		msgDealer += fmt.Sprintf("\n%s thua và bị trừ %d🌷", game.EscapeMarkdownV2(to.IconName()), reward)
		msgPlayer = fmt.Sprintf("🔻 Cái lật bài bạn và thắng\\. Bạn bị trừ %d🌷", reward)
	} else {
		msgDealer += fmt.Sprintf("\n🤝 %s và cái hoà nhau", game.EscapeMarkdownV2(to.IconName()))
		msgPlayer = "🤝 Cái lật bài bạn và hoà\\. Bạn không bị mất tiền"
	}
	msgPlayer += fmt.Sprintf("\nBài của cái: %s",
		dealer.Cards().String(false, true),
	)

	if onQuery {
		h.editMessage(m, msgDealer)
	} else {
		h.sendMessage(ToTelebotChat(dealer.ID()), msgDealer)
	}
	h.sendMessage(ToTelebotChat(to.ID()), msgPlayer)

	// Refresh dashboard after compare
	// We need to fetch the updated dashboard state
	dashMsg := h.getDealerDashboard(g, dealer)
	dashButtons := MakeDealerDashboardButtons(g, dealer)
	// We should probably edit the dashboard message if possible?
	// But `doCompare` might be triggered by a button click which we just replied to with `editMessage`.
	// If `editMessage` replaced the dashboard, we need to send a NEW dashboard?
	// The `doCompare` edits the message to show the result of comparison.
	// So we should append a new dashboard or provide a "Back" button?
	// Actually better: The result message should HAVE the dashboard buttons?
	// OR, we send a new message with the dashboard.
	h.broadcast(dealer, dashMsg, false, dashButtons...)
}

func (h *Handler) onGameFinish(g *game.Game) {
	_ = h.game.SaveToStorage()
	msg := "🏁 *Kết quả ván chơi\\!* 🏁\n\n" + g.ResultBoardMarkdownV2()
	h.broadcast(g.Room().Players(), msg, false, MakeResultButtons(g)...)
}

func (h *Handler) onPlayerPlay(g *game.Game, pg *game.PlayerInGame) {
	if pg.IsDealer() {
		msg := h.getDealerDashboard(g, pg)
		buttons := MakeDealerDashboardButtons(g, pg)
		h.broadcast(pg, msg, false, buttons...)

		go func() {
			h.broadcastLog(FilterInGamePlayers(g.AllPlayers(), pg.ID()), "👉 Tới lượt Nhà Cái")
		}()
		return
	}

	go func() {
		h.broadcastLog(FilterInGamePlayers(g.AllPlayers(), pg.ID()), "👉 Tới lượt "+game.EscapeMarkdownV2(pg.IconName()))
	}()

	h.broadcast(pg, "Tới lượt bạn: "+pg.Cards().String(false, pg.IsDealer()), false, MakePlayerButton(g, pg, false)...)
}

func (h *Handler) getDealerDashboard(g *game.Game, dealer *game.PlayerInGame) string {
	var sb strings.Builder
	sb.WriteString("🎲 *Lượt của Nhà Cái*\n\n")
	sb.WriteString(fmt.Sprintf("👑 *Bài của cái*: %s\n", dealer.Cards().String(false, true)))

	players := g.Players()

	sb.WriteString("\n👥 *Danh sách người chơi*:")
	for _, p := range players {
		status := ""
		if p.IsDone() {
			// Calculate if they won/lost?
			// Compare(dealer, p) -> but dealer might not be done yet.
			// Just show "Đã lật" (Revealed) or similar?
			status = "🏁 Đã lật"
		} else {
			status = fmt.Sprintf("%d lá", len(p.Cards()))
		}
		sb.WriteString(fmt.Sprintf("\n  • %s %s: %s", game.EscapeMarkdownV2(p.IconName()), game.EscapeMarkdownV2(p.Name()), status))
	}
	return sb.String()
}

func (h *Handler) sendChat(receivers []*game.Player, msg string) {
	h.sendChatWithOptions(receivers, msg, nil)
}

func (h *Handler) sendChatWithOptions(receivers []*game.Player, msg string, options *telebot.SendOptions) {
	for _, p := range receivers {
		h.botSend(ToTelebotChat(p.ID()), msg, options)
	}
}

func (h *Handler) sendMedia(receivers []*game.Player, what interface{}, options *telebot.SendOptions, senderBotIdx int) {
	// Prepare URL if needed (lazy load)
	var mediaURL string
	var errURL error
	attempted := false

	for _, p := range receivers {
		// Determine which bot this user is mapped to
		receiverBotIdx := 0
		if val, ok := h.userBotMap.Load(cast.ToInt64(p.ID())); ok {
			receiverBotIdx = val.(int)
		}

		// If user is on different bot than sender, we must use URL
		if receiverBotIdx != senderBotIdx {
			if !attempted {
				// Try to get URL from sender bot
				if senderBotIdx >= 0 && senderBotIdx < len(h.bots) {
					mediaURL, errURL = h.getMediaUrl(h.bots[senderBotIdx], what)
				} else {
					errURL = fmt.Errorf("invalid sender bot index")
				}
				attempted = true
			}

			if errURL == nil && mediaURL != "" {
				// Send using URL via receiver's bot
				media := h.mediaFromUrl(what, mediaURL)
				if media != nil {
					h.botSendFixed(ToTelebotChat(p.ID()), media, options, receiverBotIdx)
					continue
				}
			}
			// Fallback: try sending normally (might fail if fileID is bot-specific)
		}

		h.botSend(ToTelebotChat(p.ID()), what, options)
	}
}
