package game

import (
	"crypto/rand"
	"fmt"
	"math/big"

	"github.com/psucodervn/verixilac/internal/stringer"
)

func generateRoomID() string {
	bi, _ := rand.Int(rand.Reader, big.NewInt(100))
	return fmt.Sprintf("%02d", bi.Int64())
}

func EscapeMarkdown(text string) string {
	return stringer.EscapeMarkdown(text)
}

func EscapeMarkdownV2(text string) string {
	return stringer.EscapeMarkdownV2(text)
}
