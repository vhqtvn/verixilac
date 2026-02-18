package game

import (
	"fmt"
	"sort"
	"strings"

	"github.com/psucodervn/verixilac/internal/stringer"
)

type Rule struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Multipliers map[PlayerType]map[ResultType]int64
}

var (
	DefaultRuleID = "2"
	DefaultRules  = map[string]Rule{
		"1": {
			ID:          "1",
			Name:        "Default",
			Description: `Xì bàn: con x2, cái x1`,
			Multipliers: map[PlayerType]map[ResultType]int64{
				Participant: {
					TypeDoubleBlackJack: 2,
				},
			},
		},
		"2": {
			ID:          "2",
			Name:        "Hai Dinh",
			Description: `Xì lác, ngũ linh: x2. Xì bàn: x3. Con cái như nhau.`,
			Multipliers: map[PlayerType]map[ResultType]int64{
				Dealer: {
					TypeDoubleBlackJack: 3,
					TypeHighFive:        2,
					TypeBlackJack:       2,
				},
				Participant: {
					TypeDoubleBlackJack: 3,
					TypeHighFive:        2,
					TypeBlackJack:       2,
				},
			},
		},
	}
	SortedRuleIDs []string
	RuleListText  string
)

func init() {
	for id := range DefaultRules {
		SortedRuleIDs = append(SortedRuleIDs, id)
	}
	sort.Strings(SortedRuleIDs)

	var bf strings.Builder
	bf.WriteString(`Danh sách rules:`)
	for _, id := range SortedRuleIDs {
		r := DefaultRules[id]
		bf.WriteString(fmt.Sprintf("\n\n \\- Rule: %s, ID: %s", stringer.EscapeMarkdownV2(r.Name), stringer.EscapeMarkdownV2(id)))
		bf.WriteString(fmt.Sprintf("\n%s", stringer.EscapeMarkdownV2(r.Description)))
	}
	RuleListText = bf.String()
}
