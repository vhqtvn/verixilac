package game

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"strings"
)

func generateRoomID() string {
	bi, _ := rand.Int(rand.Reader, big.NewInt(100))
	return fmt.Sprintf("%02d", bi.Int64())
}

func EscapeMarkdown(text string) string {
	return strings.NewReplacer(
		"_", "\\_",
		"*", "\\*",
		"[", "\\[",
		"`", "\\`",
	).Replace(text)
}
