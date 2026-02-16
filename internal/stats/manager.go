package stats

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/rs/zerolog/log"
)

type Manager struct {
	filePath string
	Data     *Data
	mu       sync.RWMutex
}

func NewManager(filePath string) *Manager {
	return &Manager{
		filePath: filePath,
		Data:     NewData(),
	}
}

func (m *Manager) Load() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	b, err := os.ReadFile(m.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			// Create file if not exists
			m.Data = NewData()
			return m.save()
		}
		return err
	}

	if len(b) == 0 {
		m.Data = NewData()
		return nil
	}

	var data Data
	if err := json.Unmarshal(b, &data); err != nil {
		return err
	}
	m.Data = &data
	// Ensure maps are initialized
	if m.Data.Players == nil {
		m.Data.Players = make(map[string]*PlayerStats)
	}
	if m.Data.Pairwise == nil {
		m.Data.Pairwise = make(map[string]*PairwiseStat)
	}
	return nil
}

func (m *Manager) Save() error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.save()
}

func (m *Manager) save() error {
	dir := filepath.Dir(m.filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	b, err := json.MarshalIndent(m.Data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(m.filePath, b, 0666)
}

func (m *Manager) Update(ctx context.Context, data RoundData) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Update Global Stats
	m.Data.Global.TotalGames++

	// Check who won overall (Banker vs Players net result is complex, usually we count wins per comparison)
	// But let's look at the implementation plan: "Total games, banker wins, player wins"
	// This usually means "Banker win all" or something?
	// Or maybe just sum of all individual wins.
	// Let's count individual outcomes in RoleStats instead.
	// For global, maybe we just track total games.

	for _, pRes := range data.Stats {
		// Update Player Stats
		pStats, ok := m.Data.Players[pRes.PlayerID]
		if !ok {
			pStats = NewPlayerStats(pRes.PlayerID)
			m.Data.Players[pRes.PlayerID] = pStats
		}

		var roleStats *RoleStats
		if pRes.Role == "banker" {
			roleStats = pStats.Banker
		} else {
			roleStats = pStats.Player
		}

		roleStats.TotalGames++
		switch pRes.Result {
		case "win":
			roleStats.Wins++
			if pRes.Role == "banker" {
				m.Data.Global.BankerWins++
			} else {
				m.Data.Global.PlayerWins++
			}
		case "lose":
			roleStats.Losses++
		case "draw":
			roleStats.Draws++
			m.Data.Global.Draws++
		}

		// Update Detail Stats
		roleStats.TotalMoney += pRes.Amount
		roleStats.GetHandTypeStat(pRes.HandType).Add(pRes.Result, pRes.Amount)
		roleStats.GetScoreStat(pRes.Score).Add(pRes.Result, pRes.Amount)
		roleStats.GetCardCountStat(pRes.CardCount).Add(pRes.Result, pRes.Amount)
	}

	// Update Pairwise Stats
	for _, match := range data.Matches {
		pStat := m.Data.GetPairwise(match.Player1ID, match.Player2ID)
		pStat.TotalGames++
		if match.Result == "p1_win" {
			pStat.Player1Wins++
		} else if match.Result == "p2_win" {
			pStat.Player2Wins++
		} else {
			pStat.Draws++
		}
	}

	// Save async or sync? Sync for now to be safe.
	if err := m.save(); err != nil {
		log.Ctx(ctx).Err(err).Msg("failed to save stats")
	}
}

func (m *Manager) GetPlayerStats(playerID string) *PlayerStats {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if p, ok := m.Data.Players[playerID]; ok {
		return p
	}
	// Return empty stats if not found
	return NewPlayerStats(playerID)
}

func (m *Manager) GetGlobalStats() GlobalStats {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.Data.Global
}

// GetPairwiseStats returns the stats and a bool indicating if inputs were swapped
// If swapped=true, it means p1 input corresponds to Player2ID in struct, and p2 input to Player1ID.
// actually GetPairwise on Data already sorts them.
// So we just need to return the struct, and let the caller assume the struct's Player1ID is min(p1, p2).
func (m *Manager) GetPairwiseStats(p1, p2 string) *PairwiseStat {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.Data.GetPairwise(p1, p2)
}
