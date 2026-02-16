package game

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIconPersistence(t *testing.T) {
	// Setup temporary file for testing
	tmpFile, err := os.CreateTemp("", "data_*.json")
	require.NoError(t, err)
	defer os.Remove(tmpFile.Name()) // clean up

	// Use the temporary file path as storage file
	// Note: The storage file path is hardcoded in storage.go as const "data/data.json".
	// To test this properly without modifying the const, we might need to make it configurable
	// or use a different approach. However, for now we will try to backup and restore the original file if it exists,
	// and use the constant path.

	originalStorageFile := "data/data.json"
	backupFile := "data/data.json.bak"

	// Check if original file exists and backup
	if _, err := os.Stat(originalStorageFile); err == nil {
		input, err := os.ReadFile(originalStorageFile)
		require.NoError(t, err)
		err = os.WriteFile(backupFile, input, 0644)
		require.NoError(t, err)
		defer func() {
			// Restore backup
			input, err := os.ReadFile(backupFile)
			if err == nil {
				os.WriteFile(originalStorageFile, input, 0644)
				os.Remove(backupFile)
			}
		}()
	}

	// Ensure data directory exists
	err = os.MkdirAll("data", 0755)
	require.NoError(t, err)

	// Clean up the storage file for the test
	os.Remove(originalStorageFile)
	defer os.Remove(originalStorageFile)

	// 1. Create Manager and Player
	m1 := NewManager(100, 10, time.Minute)
	playerID := "p1"
	p1 := m1.PlayerRegister(context.Background(), playerID, "Player One")

	// 2. Set Icon
	icon := "🚀"
	p1.SetIcon(icon)
	assert.Equal(t, icon, p1.Icon())

	// 3. Save to storage
	err = m1.SaveToStorage()
	require.NoError(t, err)

	// 4. Create new Manager and Load from storage
	m2 := NewManager(100, 10, time.Minute)
	err = m2.LoadFromStorage()
	require.NoError(t, err)

	// 5. Verify persistence
	p2 := m2.FindPlayer(context.Background(), playerID)
	require.NotNil(t, p2)
	assert.Equal(t, icon, p2.Icon(), "Icon should be persisted")
}
