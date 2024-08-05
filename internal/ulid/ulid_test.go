package ulid_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/helmedeiros/model-registry/internal/ulid"
)

func TestNewProduces26CharULIDs(t *testing.T) {
	g := ulid.New()
	id, err := g.New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if len(id) != 26 {
		t.Fatalf("len=%d want 26: %q", len(id), id)
	}
}

func TestIDsSortLexicographicallyByTime(t *testing.T) {
	clock := stepClock(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	g := ulid.New(ulid.WithClock(clock))

	ids := make([]string, 5)
	for i := range ids {
		id, err := g.New()
		if err != nil {
			t.Fatal(err)
		}
		ids[i] = id
	}
	for i := 1; i < len(ids); i++ {
		if ids[i] <= ids[i-1] {
			t.Fatalf("ULIDs not strictly ascending: %v", ids)
		}
	}
}

func TestSameMillisecondMonotonicallyIncrements(t *testing.T) {
	fixed := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	g := ulid.New(
		ulid.WithClock(func() time.Time { return fixed }),
		// All-zero entropy so the increment is observable.
		ulid.WithEntropy(bytes.NewReader(zeros(1024))),
	)
	a, _ := g.New()
	b, _ := g.New()
	c, _ := g.New()
	if !(a < b && b < c) {
		t.Fatalf("monotonic increment broken: %s %s %s", a, b, c)
	}
}

func TestNegativeUnixMSErrors(t *testing.T) {
	pre := time.Date(1969, 1, 1, 0, 0, 0, 0, time.UTC)
	g := ulid.New(ulid.WithClock(func() time.Time { return pre }))
	if _, err := g.New(); err == nil {
		t.Fatal("expected error for pre-epoch clock")
	}
}

func TestEntropyReadErrorPropagates(t *testing.T) {
	wantErr := errors.New("synthetic entropy failure")
	g := ulid.New(ulid.WithEntropy(failingReader{err: wantErr}))
	_, err := g.New()
	if !errors.Is(err, wantErr) {
		t.Fatalf("err=%v want wantErr", err)
	}
}

func TestEncodedCharactersAreCrockfordBase32(t *testing.T) {
	g := ulid.New()
	id, _ := g.New()
	const allowed = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	for _, r := range id {
		if !strings.ContainsRune(allowed, r) {
			t.Fatalf("ULID %q contains non-base32 char %q", id, r)
		}
	}
}

// --- helpers ---

func stepClock(start time.Time) func() time.Time {
	t := start
	return func() time.Time {
		t = t.Add(time.Millisecond)
		return t
	}
}

func zeros(n int) []byte { return make([]byte, n) }

type failingReader struct{ err error }

func (f failingReader) Read(_ []byte) (int, error) { return 0, f.err }
