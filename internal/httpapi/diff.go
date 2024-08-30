package httpapi

import (
	"errors"
	"net/http"
	"sort"

	"github.com/helmedeiros/model-registry/internal/store"
)

// Diff returns the rule-provenance diff between two artifact bundles.
// The handler stays format-agnostic: it compares the Rules slices
// stored alongside each bundle's Metadata and reports added /
// removed / modified entries keyed by RuleID.
func Diff(reader store.Reader) http.Handler {
	if reader == nil {
		panic("httpapi.Diff: reader required")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		from := store.Hash(r.PathValue("from"))
		to := store.Hash(r.PathValue("to"))
		if from == "" || to == "" {
			writeError(w, http.StatusBadRequest, "hash_required")
			return
		}
		if from == to {
			writeError(w, http.StatusBadRequest, "identical_hashes")
			return
		}

		fromBundle, code, reason, err := loadBundle(r, reader, from, "from")
		if err != nil {
			writeError(w, code, reason)
			return
		}
		toBundle, code, reason, err := loadBundle(r, reader, to, "to")
		if err != nil {
			writeError(w, code, reason)
			return
		}
		writeJSON(w, http.StatusOK, diffArtifacts(fromBundle, toBundle))
	})
}

func loadBundle(r *http.Request, reader store.Reader, h store.Hash, side string) (store.Bundle, int, string, error) {
	b, err := reader.GetBundle(r.Context(), h)
	if errors.Is(err, store.ErrNotFound) {
		return store.Bundle{}, http.StatusNotFound, side + "_not_found", err
	}
	if err != nil {
		return store.Bundle{}, http.StatusInternalServerError, "get_failed", err
	}
	return b, 0, "", nil
}

func diffArtifacts(from, to store.Bundle) ArtifactDiffView {
	out := ArtifactDiffView{
		From:     string(from.Hash),
		To:       string(to.Hash),
		Added:    []RuleProvenanceJSON{},
		Removed:  []RuleProvenanceJSON{},
		Modified: []RuleDiffEntryView{},
	}

	fromByID := indexRules(from.Metadata.Rules)
	toByID := indexRules(to.Metadata.Rules)

	for id, toRule := range toByID {
		fromRule, existed := fromByID[id]
		if !existed {
			out.Added = append(out.Added, ruleProvenanceJSON(toRule))
			continue
		}
		if !sameProvenance(fromRule, toRule) {
			out.Modified = append(out.Modified, RuleDiffEntryView{
				From: ruleProvenanceJSON(fromRule),
				To:   ruleProvenanceJSON(toRule),
			})
		}
	}
	for id, fromRule := range fromByID {
		if _, kept := toByID[id]; !kept {
			out.Removed = append(out.Removed, ruleProvenanceJSON(fromRule))
		}
	}
	sortByRuleID(out.Added)
	sortByRuleID(out.Removed)
	sort.Slice(out.Modified, func(i, j int) bool { return out.Modified[i].To.RuleID < out.Modified[j].To.RuleID })
	return out
}

func indexRules(rs []store.RuleProvenance) map[string]store.RuleProvenance {
	out := make(map[string]store.RuleProvenance, len(rs))
	for _, r := range rs {
		out[r.RuleID] = r
	}
	return out
}

func sameProvenance(a, b store.RuleProvenance) bool {
	return a.Author == b.Author &&
		a.SourceCommitSHA == b.SourceCommitSHA &&
		a.PRURL == b.PRURL &&
		a.Description == b.Description &&
		a.LastModified.Equal(b.LastModified)
}

func sortByRuleID(rs []RuleProvenanceJSON) {
	sort.Slice(rs, func(i, j int) bool { return rs[i].RuleID < rs[j].RuleID })
}
