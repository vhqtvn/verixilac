package game

import (
	"fmt"
	"testing"
	"time"
)

func TestMessageFormats(t *testing.T) {
	// Setup dummy game
	dealer := NewPlayer("dealer_id", "Dealer", 1000)
	rule := DefaultRules["1"] // Xì dách
	room := &Room{id: "ROOM_1"}

	g := NewGame(dealer, room, &rule, 5000, 30*time.Second)

	// Add players
	p1 := NewPlayer("p1_id", "Player 1", 500)
	g.PlayerBet(p1, 100)

	p2 := NewPlayer("p2_id", "Player 2", 2000)
	g.PlayerBet(p2, 200)

	fmt.Println("--- PreparingBoard ---")
	fmt.Println(g.PreparingBoard())
	fmt.Println("----------------------")

	// Deal
	g.Deal()

	// Force some cards for better visualization (if possible, but randomized is fine for format check)
	// We can't easily force cards without exporting fields or using unsafe, so we rely on what we get.

	fmt.Println("--- CurrentBoard ---")
	fmt.Println(g.CurrentBoard())
	fmt.Println("--------------------")

	// Simulate game end
	g.status.Store(uint32(Finished))

	fmt.Println("--- ResultBoard ---")
	fmt.Println(g.ResultBoard())
	fmt.Println("-------------------")
}
