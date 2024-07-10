package store

import (
	"errors"
	"testing"
)

func TestPutRequestValidate(t *testing.T) {
	tests := []struct {
		name string
		req  PutRequest
		want error
	}{
		{
			name: "happy_path",
			req:  PutRequest{SourceBytes: []byte("x"), ContentType: ContentTypeCSV},
		},
		{
			name: "missing_source",
			req:  PutRequest{ContentType: ContentTypeCSV},
			want: ErrSourceRequired,
		},
		{
			name: "empty_source_slice",
			req:  PutRequest{SourceBytes: []byte{}, ContentType: ContentTypeCSV},
			want: ErrSourceRequired,
		},
		{
			name: "missing_content_type",
			req:  PutRequest{SourceBytes: []byte("x")},
			want: ErrContentTypeRequired,
		},
		{
			name: "unknown_content_type_sentinel",
			req:  PutRequest{SourceBytes: []byte("x"), ContentType: ContentTypeUnknown},
			want: ErrContentTypeRequired,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.req.Validate()
			if !errors.Is(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
