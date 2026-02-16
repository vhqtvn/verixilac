package stats

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestManager_Update(t *testing.T) {
	tmpFile := "test_stats.json"
	defer os.Remove(tmpFile)

	m := NewManager(tmpFile)

	// Test Case 1: Simple Round
	rd := RoundData{
		Stats: []RoundPlayerResult{
			{
				PlayerID:  "dealer",
				Role:      "banker",
				Result:    "win",
				HandType:  "xilac",
				Score:     21,
				CardCount: 2,
			},
			{
				PlayerID:  "p1",
				Role:      "player",
				Result:    "lose",
				HandType:  "thuong",
				Score:     18,
				CardCount: 2,
			},
		},
		Matches: []MatchResult{
			{
				Player1ID: "dealer",
				Player2ID: "p1",
				Result:    "p1_win",
			},
		},
	}

	m.Update(context.Background(), rd)

	// Verify Global Stats
	// TotalGames should be 1. BankerWins 1, PlayerWins 0 in global (based on implementation logic)
	// Actually logic was: if Role=="banker" && Result=="win" -> BankerWins++
	assert.Equal(t, int64(1), m.Data.Global.TotalGames)
	assert.Equal(t, int64(1), m.Data.Global.BankerWins)
	assert.Equal(t, int64(0), m.Data.Global.PlayerWins)

	// Verify Dealer Stats
	dStats := m.GetPlayerStats("dealer").Banker
	assert.Equal(t, int64(1), dStats.TotalGames)
	assert.Equal(t, int64(1), dStats.Wins)
	assert.Equal(t, int64(1), dStats.GetHandTypeStat("xilac").Occurrences)
	assert.Equal(t, int64(1), dStats.GetHandTypeStat("xilac").Wins)

	// Verify P1 Stats
	p1Stats := m.GetPlayerStats("p1").Player
	assert.Equal(t, int64(1), p1Stats.TotalGames)
	assert.Equal(t, int64(1), p1Stats.Losses)
	assert.Equal(t, int64(1), p1Stats.GetScoreStat(18).Occurrences)
	assert.Equal(t, int64(1), p1Stats.GetScoreStat(18).Losses)

	// Verify Pairwise
	pw := m.Data.GetPairwise("dealer", "p1")
	assert.Equal(t, int64(1), pw.TotalGames)
	assert.Equal(t, int64(1), pw.Player1Wins) // "p1_win" means Player win? No, Wait.
	// In Update logic:
	// if match.Result == "p1_win" { pStat.Player1Wins++ }
	// MatchResult input was { Player1ID: "dealer", Player2ID: "p1", Result: "p1_win" }
	// So Player1 (dealer) won. Correct.

	// Test Persistence
	m2 := NewManager(tmpFile)
	err := m2.Load()
	assert.NoError(t, err)
	assert.Equal(t, int64(1), m2.Data.Global.TotalGames)
	assert.Equal(t, int64(1), m2.GetPlayerStats("dealer").Banker.Wins)

	// Test Pairwise Retrieval
	pw2 := m2.GetPairwiseStats("dealer", "p1")
	assert.Equal(t, int64(1), pw2.Player1Wins)

	// Test Pairwise String
	// "dealer" vs "p1" -> "Player1 wins: 1", "Player2 wins: 0"
	// if we pass names "DealerName", "P1Name", it should print "DealerName thắng: 1"
	str := pw2.String("DealerName", "P1Name")
	assert.Contains(t, str, "DealerName thắng: 1")
}
