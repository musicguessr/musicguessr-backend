package deck

import "testing"

func TestExtractYtID(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{
			name:  "bare 11-char ID",
			input: "dQw4w9WgXcQ",
			want:  "dQw4w9WgXcQ",
		},
		{
			name:  "watch URL",
			input: "https://www.youtube.com/watch?v=dQw4w9WgXcQ",
			want:  "dQw4w9WgXcQ",
		},
		{
			name:  "watch URL with extra params",
			input: "https://www.youtube.com/watch?v=dQw4w9WgXcQ&t=42",
			want:  "dQw4w9WgXcQ",
		},
		{
			name:  "short youtu.be URL",
			input: "https://youtu.be/dQw4w9WgXcQ",
			want:  "dQw4w9WgXcQ",
		},
		{
			name:  "shorts URL",
			input: "https://www.youtube.com/shorts/dQw4w9WgXcQ",
			want:  "dQw4w9WgXcQ",
		},
		{
			name:  "embed URL",
			input: "https://www.youtube.com/embed/dQw4w9WgXcQ",
			want:  "dQw4w9WgXcQ",
		},
		{
			name:  "live URL",
			input: "https://www.youtube.com/live/dQw4w9WgXcQ",
			want:  "dQw4w9WgXcQ",
		},
		{
			name:    "ID too short",
			input:   "abc",
			wantErr: true,
		},
		{
			name:    "non-YouTube domain",
			input:   "https://example.com/video/dQw4w9WgXcQ",
			wantErr: true,
		},
		{
			name:    "empty string",
			input:   "",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := extractYtID(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Errorf("expected error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
