package telegram

import (
	"github.com/spf13/cast"
	"gopkg.in/tucnak/telebot.v2"

	"github.com/psucodervn/verixilac/internal/game"
)

func ToTelebotChats(ids ...string) []*telebot.Chat {
	cs := make([]*telebot.Chat, len(ids))
	for i, id := range ids {
		cs[i] = &telebot.Chat{ID: cast.ToInt64(id)}
	}
	return cs
}

func ToTelebotChat(id string) *telebot.Chat {
	return &telebot.Chat{ID: cast.ToInt64(id)}
}

func FilterPlayers(players []*game.Player, ids ...string) []*game.Player {
	m := make(map[string]struct{})
	for _, id := range ids {
		m[id] = struct{}{}
	}
	var ps []*game.Player
	for i := 0; i < len(players); i++ {
		if _, exists := m[players[i].ID()]; !exists {
			ps = append(ps, players[i])
		}
	}
	return ps
}

func FilterInGamePlayers(players []*game.PlayerInGame, ids ...string) []*game.Player {
	m := make(map[string]struct{})
	for _, id := range ids {
		m[id] = struct{}{}
	}
	var ps []*game.Player
	for i := 0; i < len(players); i++ {
		if _, exists := m[players[i].ID()]; !exists {
			ps = append(ps, players[i].Player)
		}
	}
	return ps
}
