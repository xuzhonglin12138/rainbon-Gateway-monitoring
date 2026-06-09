package model

import "testing"

func TestParseWindowAllowsOnlyHotWindows(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		want       Window
		wantBucket int
		wantErr    bool
	}{
		{name: "default empty window", input: "", want: Window5m, wantBucket: 300},
		{name: "five minutes", input: "5m", want: Window5m, wantBucket: 300},
		{name: "ten minutes", input: "10m", want: Window10m, wantBucket: 600},
		{name: "thirty minutes", input: "30m", want: Window30m, wantBucket: 1800},
		{name: "reject one hour", input: "1h", wantErr: true},
		{name: "reject custom window", input: "12m", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseWindow(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseWindow(%q) expected error", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseWindow(%q) unexpected error: %v", tt.input, err)
			}
			if got != tt.want {
				t.Fatalf("ParseWindow(%q) = %q; want %q", tt.input, got, tt.want)
			}
			if got.BucketCount() != tt.wantBucket {
				t.Fatalf("BucketCount() = %d; want %d", got.BucketCount(), tt.wantBucket)
			}
		})
	}
}
