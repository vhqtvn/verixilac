package stats

import (
	"fmt"
	"sort"
	"strings"

	"github.com/psucodervn/verixilac/internal/stringer"
)

type (
	// GlobalStats stores system-wide statistics
	GlobalStats struct {
		TotalGames int64 `json:"totalGames"`
		BankerWins int64 `json:"bankerWins"`
		PlayerWins int64 `json:"playerWins"`
		Draws      int64 `json:"draws"`
	}

	// PairwiseStats stores statistics between two players
	// Key is "PlayerID1_PlayerID2" where PlayerID1 < PlayerID2
	PairwiseStat struct {
		Player1ID   string `json:"player1Id"`
		Player2ID   string `json:"player2Id"`
		TotalGames  int64  `json:"totalGames"`
		Player1Wins int64  `json:"player1Wins"`
		Player2Wins int64  `json:"player2Wins"`
		Draws       int64  `json:"draws"`
	}

	// RoleStats stores statistics for a specific role (Banker or Player)
	// RoleStats stores statistics for a specific role (Banker or Player)
	RoleStats struct {
		TotalGames int64 `json:"totalGames"`
		Wins       int64 `json:"wins"`
		Losses     int64 `json:"losses"`
		Draws      int64 `json:"draws"`
		TotalMoney int64 `json:"totalMoney"`

		// HandTypeStats counts occurrences of specific hand types
		// Keys: "xilac", "xiban", "ngulinh", "chay", "thuong"
		HandTypeStats map[string]*DetailStat `json:"handTypeStats"`

		// ScoreStats counts occurrences of specific scores (2-21)
		// Keys: "2", "3", ..., "21"
		ScoreStats map[string]*DetailStat `json:"scoreStats"`

		// CardCountStats counts occurrences of specific card counts
		// Keys: "2", "3", "4", "5"
		CardCountStats map[string]*DetailStat `json:"cardCountStats"`
	}

	// DetailStat stores detailed stats for a specific category (e.g., Score 21, 5 Cards)
	DetailStat struct {
		Occurrences int64 `json:"occurrences"`
		Wins        int64 `json:"wins"`
		Losses      int64 `json:"losses"`
		Draws       int64 `json:"draws"`
		TotalMoney  int64 `json:"totalMoney"`
	}

	// PlayerStats aggregates all stats for a single player
	PlayerStats struct {
		PlayerID string     `json:"playerId"`
		Banker   *RoleStats `json:"banker"`
		Player   *RoleStats `json:"player"`
	}

	// Data is the root structure for stats.json
	Data struct {
		Global   GlobalStats              `json:"global"`
		Players  map[string]*PlayerStats  `json:"players"`
		Pairwise map[string]*PairwiseStat `json:"pairwise"`
	}

	// Input Data Structures for decoupling
	RoundPlayerResult struct {
		PlayerID string
		Role     string // "banker", "player"
		Result   string // "win", "lose", "draw"
		Amount   int64  // Positive for win, negative for lose

		HandType  string // "xilac", "xiban", "ngulinh", "chay", "thuong"
		Score     int
		CardCount int
	}

	MatchResult struct {
		Player1ID string
		Player2ID string
		Result    string // "p1_win", "p2_win", "draw"
	}

	RoundData struct {
		Stats   []RoundPlayerResult
		Matches []MatchResult
	}
)

func NewData() *Data {
	return &Data{
		Global:   GlobalStats{},
		Players:  make(map[string]*PlayerStats),
		Pairwise: make(map[string]*PairwiseStat),
	}
}

func NewPlayerStats(id string) *PlayerStats {
	return &PlayerStats{
		PlayerID: id,
		Banker:   NewRoleStats(),
		Player:   NewRoleStats(),
	}
}

func NewRoleStats() *RoleStats {
	return &RoleStats{
		HandTypeStats:  make(map[string]*DetailStat),
		ScoreStats:     make(map[string]*DetailStat),
		CardCountStats: make(map[string]*DetailStat),
	}
}

func (s *DetailStat) Add(result string, amount int64) {
	s.Occurrences++
	s.TotalMoney += amount
	switch result {
	case "win":
		s.Wins++
	case "lose":
		s.Losses++
	case "draw":
		s.Draws++
	}
}

func (s *RoleStats) GetHandTypeStat(key string) *DetailStat {
	if _, ok := s.HandTypeStats[key]; !ok {
		s.HandTypeStats[key] = &DetailStat{}
	}
	return s.HandTypeStats[key]
}

func (s *RoleStats) GetScoreStat(score int) *DetailStat {
	key := fmt.Sprintf("%d", score)
	if _, ok := s.ScoreStats[key]; !ok {
		s.ScoreStats[key] = &DetailStat{}
	}
	return s.ScoreStats[key]
}

func (s *RoleStats) GetCardCountStat(count int) *DetailStat {
	key := fmt.Sprintf("%d", count)
	if _, ok := s.CardCountStats[key]; !ok {
		s.CardCountStats[key] = &DetailStat{}
	}
	return s.CardCountStats[key]
}

func (d *Data) GetPairwise(p1, p2 string) *PairwiseStat {
	if p1 > p2 {
		p1, p2 = p2, p1
	}
	key := fmt.Sprintf("%s_%s", p1, p2)
	if _, ok := d.Pairwise[key]; !ok {
		d.Pairwise[key] = &PairwiseStat{
			Player1ID: p1,
			Player2ID: p2,
		}
	}
	return d.Pairwise[key]
}

// String returns a formatted string representation of PlayerStats
// This is a basic implementation, can be improved for better Telegram display
func (p *PlayerStats) String() string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📊 Thống kê: %s\n", stringer.EscapeMarkdownV2(p.PlayerID))) // Name should be resolved by caller if possible, or we store name

	sb.WriteString("\n🅰️ Vai trò CÁI \\(Banker\\):\n")
	sb.WriteString(p.Banker.String())

	sb.WriteString("\n🅱️ Vai trò CON \\(Player\\):\n")
	sb.WriteString(p.Player.String())

	return sb.String()
}

func (r *RoleStats) String() string {
	var sb strings.Builder
	winRate := 0.0
	if r.TotalGames > 0 {
		winRate = float64(r.Wins) / float64(r.TotalGames) * 100
	}
	sb.WriteString(fmt.Sprintf("  \\- Tổng ván: %d \\(Thắng: %d \\| Thua: %d \\| Hoà: %d\\)\n", r.TotalGames, r.Wins, r.Losses, r.Draws))
	sb.WriteString(fmt.Sprintf("  \\- Tổng tiền: %s🌷\n", stringer.EscapeMarkdownV2(fmt.Sprintf("%+d", r.TotalMoney))))
	sb.WriteString(fmt.Sprintf("  \\- Tỷ lệ thắng: %.2f%%\n", winRate))

	// Top interesting stats could go here, e.g. "Xì lác: 5, Ngũ linh: 1"
	keys := make([]string, 0, len(r.HandTypeStats))
	for k := range r.HandTypeStats {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	sb.WriteString("  \\- Chi tiết bài:\n")
	hasSpecial := false
	for _, k := range keys {
		s := r.HandTypeStats[k]
		if s.Occurrences > 0 {
			sb.WriteString(fmt.Sprintf("    \\+ %s: %d \\(%s🌷\\)\n", k, s.Occurrences, stringer.EscapeMarkdownV2(fmt.Sprintf("%+d", s.TotalMoney))))
			hasSpecial = true
		}
	}
	if !hasSpecial {
		sb.WriteString("    \\(Chưa có\\)\n")
	}

	sb.WriteString("  \\- Số lượng lá:\n")
	cardKeys := make([]string, 0, len(r.CardCountStats))
	for k := range r.CardCountStats {
		cardKeys = append(cardKeys, k)
	}
	sort.Strings(cardKeys)
	hasCards := false
	for _, k := range cardKeys {
		s := r.CardCountStats[k]
		if s.Occurrences > 0 {
			sb.WriteString(fmt.Sprintf("    \\+ %s lá: %d \\(%s🌷\\)\n", k, s.Occurrences, stringer.EscapeMarkdownV2(fmt.Sprintf("%+d", s.TotalMoney))))
			hasCards = true
		}
	}
	if !hasCards {
		sb.WriteString("    \\(Chưa có\\)\n")
	}

	return sb.String()
}

func (p *PairwiseStat) String(p1Name, p2Name string) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📊 Đối đầu: %s vs %s\n", stringer.EscapeMarkdownV2(p1Name), stringer.EscapeMarkdownV2(p2Name)))

	sb.WriteString(fmt.Sprintf("\\- Tổng số ván: %d\n", p.TotalGames))
	sb.WriteString(fmt.Sprintf("\\- %s thắng: %d\n", stringer.EscapeMarkdownV2(p1Name), p.Player1Wins))
	sb.WriteString(fmt.Sprintf("\\- %s thắng: %d\n", stringer.EscapeMarkdownV2(p2Name), p.Player2Wins))
	sb.WriteString(fmt.Sprintf("\\- Hoà: %d\n", p.Draws))

	return sb.String()
}
