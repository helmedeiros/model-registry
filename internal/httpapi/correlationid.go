package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
)

// randRead is the source of UUID bytes. Production code uses
// crypto/rand; package-internal tests substitute a failing reader to
// exercise the error path. Tests in this package do not use
// t.Parallel().
var randRead = rand.Read

type correlationIDKey struct{}

func withCorrelationContext(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, correlationIDKey{}, id)
}

// CorrelationIDFromContext returns the correlation ID stored on ctx
// by WithCorrelationID, or empty string when no ID has been set.
func CorrelationIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(correlationIDKey{}).(string)
	return id
}

// WithCorrelationID guarantees every downstream handler sees a
// correlation ID on its request context. The ID comes from the
// X-Correlation-ID request header when present; otherwise it is
// minted as a crypto/rand-backed UUID v4. The active ID is echoed on
// the response header regardless of source. If UUID minting fails
// (essentially impossible on healthy systems), the middleware
// responds 500 with an opaque body.
func WithCorrelationID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(CorrelationIDHeader)
		if id == "" {
			generated, err := generateUUID()
			if err != nil {
				writeError(w, http.StatusInternalServerError, "internal")
				return
			}
			id = generated
		}
		w.Header().Set(CorrelationIDHeader, id)
		next.ServeHTTP(w, r.WithContext(withCorrelationContext(r.Context(), id)))
	})
}

// generateUUID returns an RFC 4122 v4 UUID encoded directly into a
// fixed 36-byte buffer so the minting path stays a single small
// allocation (the returned string) rather than the 5x hex.EncodeToString
// + fmt.Sprintf chain a naive implementation produces.
func generateUUID() (string, error) {
	var raw [16]byte
	if _, err := randRead(raw[:]); err != nil {
		return "", err
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80

	var out [36]byte
	hex.Encode(out[0:8], raw[0:4])
	out[8] = '-'
	hex.Encode(out[9:13], raw[4:6])
	out[13] = '-'
	hex.Encode(out[14:18], raw[6:8])
	out[18] = '-'
	hex.Encode(out[19:23], raw[8:10])
	out[23] = '-'
	hex.Encode(out[24:36], raw[10:16])
	return string(out[:]), nil
}
