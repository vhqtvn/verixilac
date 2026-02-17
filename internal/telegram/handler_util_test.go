package telegram

import (
	"testing"
)

func TestRetryAfterRegex(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		want      string
		wantMatch bool
	}{
		{
			name:      "Standard retry error",
			input:     "telegram: retry after 20041 (429)",
			want:      "20041",
			wantMatch: true,
		},
		{
			name:      "Short retry error",
			input:     "telegram: retry after 5 (429)",
			want:      "5",
			wantMatch: true,
		},
		{
			name:      "Other error",
			input:     "telegram: bad request (400)",
			wantMatch: false,
		},
		{
			name:      "Arbitrary text",
			input:     "some other error",
			wantMatch: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matches := retryAfterRegex.FindStringSubmatch(tt.input)
			if tt.wantMatch {
				if len(matches) < 2 {
					t.Errorf("FindStringSubmatch(%q) returned no matches, want %q", tt.input, tt.want)
					return
				}
				if matches[1] != tt.want {
					t.Errorf("FindStringSubmatch(%q) = %q, want %q", tt.input, matches[1], tt.want)
				}
			} else {
				if len(matches) > 0 {
					t.Errorf("FindStringSubmatch(%q) unexpectedly matched: %v", tt.input, matches)
				}
			}
		})
	}
}
