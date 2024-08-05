// Package ulid generates ULIDs (Universally Unique
// Lexicographically Sortable Identifiers) for audit log entries.
// The encoding follows the public ULID spec: 48-bit big-endian
// timestamp (ms since Unix epoch) + 80-bit randomness, encoded in
// Crockford base32 producing a fixed 26-character string. ULIDs sort
// lexicographically by timestamp, then by randomness within the same
// millisecond — the property memaudit and fsaudit's history sort
// tiebreakers depend on.
package ulid

import (
	"crypto/rand"
	"errors"
	"io"
	"sync"
	"time"
)

// Generator produces ULIDs. Safe for concurrent use.
type Generator struct {
	mu      sync.Mutex
	now     func() time.Time
	entropy io.Reader
	lastMS  int64
	lastRnd [10]byte
}

// Option configures a Generator at construction.
type Option func(*Generator)

// WithClock injects a clock for deterministic ULIDs in tests.
func WithClock(now func() time.Time) Option {
	return func(g *Generator) { g.now = now }
}

// WithEntropy injects a non-crypto/rand source for deterministic
// ULIDs in tests.
func WithEntropy(r io.Reader) Option {
	return func(g *Generator) { g.entropy = r }
}

// New returns a Generator with crypto/rand entropy and time.Now.
func New(opts ...Option) *Generator {
	g := &Generator{
		now:     time.Now,
		entropy: rand.Reader,
	}
	for _, opt := range opts {
		opt(g)
	}
	return g
}

// New generates one ULID. Returns the 26-character encoded string.
// If two calls land in the same millisecond, the entropy is
// monotonically incremented to keep the lexicographic order strictly
// ascending — required so sort-by-ID matches sort-by-time within a
// bucket.
func (g *Generator) New() (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	ms := g.now().UnixMilli()
	if ms < 0 {
		return "", errors.New("ulid: time before unix epoch")
	}

	var rnd [10]byte
	if ms == g.lastMS {
		incrementBytes(&g.lastRnd)
		rnd = g.lastRnd
	} else {
		if _, err := io.ReadFull(g.entropy, rnd[:]); err != nil {
			return "", err
		}
		g.lastRnd = rnd
		g.lastMS = ms
	}

	return encode(ms, rnd), nil
}

// incrementBytes adds one to the 80-bit big-endian random field with
// carry. Wrap-around (~10^24 entries in one millisecond) is
// effectively impossible for our throughput.
func incrementBytes(b *[10]byte) {
	for i := 9; i >= 0; i-- {
		b[i]++
		if b[i] != 0 {
			return
		}
	}
}

// encode produces the 26-character Crockford base32 string the ULID
// spec defines.
func encode(ms int64, rnd [10]byte) string {
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	var out [26]byte
	// 48-bit timestamp → 10 base32 characters.
	out[0] = alphabet[(ms>>45)&0x1F]
	out[1] = alphabet[(ms>>40)&0x1F]
	out[2] = alphabet[(ms>>35)&0x1F]
	out[3] = alphabet[(ms>>30)&0x1F]
	out[4] = alphabet[(ms>>25)&0x1F]
	out[5] = alphabet[(ms>>20)&0x1F]
	out[6] = alphabet[(ms>>15)&0x1F]
	out[7] = alphabet[(ms>>10)&0x1F]
	out[8] = alphabet[(ms>>5)&0x1F]
	out[9] = alphabet[ms&0x1F]
	// 80-bit randomness → 16 base32 characters.
	out[10] = alphabet[(rnd[0]&0xF8)>>3]
	out[11] = alphabet[((rnd[0]&0x07)<<2)|((rnd[1]&0xC0)>>6)]
	out[12] = alphabet[(rnd[1]&0x3E)>>1]
	out[13] = alphabet[((rnd[1]&0x01)<<4)|((rnd[2]&0xF0)>>4)]
	out[14] = alphabet[((rnd[2]&0x0F)<<1)|((rnd[3]&0x80)>>7)]
	out[15] = alphabet[(rnd[3]&0x7C)>>2]
	out[16] = alphabet[((rnd[3]&0x03)<<3)|((rnd[4]&0xE0)>>5)]
	out[17] = alphabet[rnd[4]&0x1F]
	out[18] = alphabet[(rnd[5]&0xF8)>>3]
	out[19] = alphabet[((rnd[5]&0x07)<<2)|((rnd[6]&0xC0)>>6)]
	out[20] = alphabet[(rnd[6]&0x3E)>>1]
	out[21] = alphabet[((rnd[6]&0x01)<<4)|((rnd[7]&0xF0)>>4)]
	out[22] = alphabet[((rnd[7]&0x0F)<<1)|((rnd[8]&0x80)>>7)]
	out[23] = alphabet[(rnd[8]&0x7C)>>2]
	out[24] = alphabet[((rnd[8]&0x03)<<3)|((rnd[9]&0xE0)>>5)]
	out[25] = alphabet[rnd[9]&0x1F]
	return string(out[:])
}
