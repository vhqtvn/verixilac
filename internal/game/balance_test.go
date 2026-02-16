package game

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestNegativeBalance(t *testing.T) {
	m := NewManager(100, 10, time.Second)
	ctx := context.Background()

	// 1. Dealer with negative balance can create game
	dealer := m.PlayerRegister(ctx, "dealer", "Dealer")
	dealer.AddBalance(-100) // Balance is -100
	assert.Equal(t, int64(-100), dealer.Balance())

	room, err := m.NewRoom(ctx, dealer)
	assert.NoError(t, err)
	assert.NotNil(t, room)

	game, err := m.NewGame(room, dealer)
	assert.NoError(t, err)
	assert.NotNil(t, game)

	// 2. Player with negative balance can bet
	player := m.PlayerRegister(ctx, "player", "Player")
	player.AddBalance(-50) // Balance is -50
	assert.Equal(t, int64(-50), player.Balance())

	err = m.JoinRoom(ctx, player, room)
	assert.NoError(t, err)

	err = m.PlayerBet(ctx, game, player, 20)
	assert.NoError(t, err)

	pg := game.FindPlayer(player.ID())
	assert.NotNil(t, pg)
	assert.Equal(t, uint64(20), pg.BetAmount())
}
