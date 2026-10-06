package api

import (
	"net/http"

	"github.com/rafaelvieiras/playheadtv/backend/internal/cutruleset"
	"github.com/rafaelvieiras/playheadtv/backend/internal/materializer"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

// registerCutRuleSets mounts the cut rule set resource: ordered rules that
// decide where inside an item a break goes (docs/ARCHITECTURE.md "Cut points").
// The materializer cuts items by the stored sets, so every write retriggers it,
// like a break template does. POST /cut-rule-sets/preview evaluates a rule
// list for a sample item without storing anything.
func registerCutRuleSets(mux *http.ServeMux, db storage.DB, mat *materializer.Materializer) {
	mux.HandleFunc("POST /cut-rule-sets", createCutRuleSet(db, mat))
	mux.HandleFunc("GET /cut-rule-sets", listCutRuleSets(db))
	mux.HandleFunc("POST /cut-rule-sets/preview", previewCutRuleSet())
	mux.HandleFunc("GET /cut-rule-sets/{id}", getCutRuleSet(db))
	mux.HandleFunc("PUT /cut-rule-sets/{id}", updateCutRuleSet(db, mat))
	mux.HandleFunc("DELETE /cut-rule-sets/{id}", deleteCutRuleSet(db, mat))
}

// writeCutRuleSetError maps a store error to a response: bad input is a 400
// with the specific message, anything else follows handleDBError.
func writeCutRuleSetError(w http.ResponseWriter, err error) {
	if cutruleset.IsValidation(err) {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	handleDBError(w, err)
}

func createCutRuleSet(db storage.DB, mat *materializer.Materializer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in cutruleset.Input
		if err := decodeJSON(r, &in); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		set, err := cutruleset.Create(r.Context(), db, DefaultTenantID, in)
		if err != nil {
			writeCutRuleSetError(w, err)
			return
		}
		mat.Trigger()
		writeJSON(w, http.StatusCreated, set)
	}
}

func listCutRuleSets(db storage.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sets, err := cutruleset.List(r.Context(), db, DefaultTenantID)
		if err != nil {
			handleDBError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, sets)
	}
}

func getCutRuleSet(db storage.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		set, err := cutruleset.Get(r.Context(), db, DefaultTenantID, r.PathValue("id"))
		if err != nil {
			handleDBError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, set)
	}
}

func updateCutRuleSet(db storage.DB, mat *materializer.Materializer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in cutruleset.Input
		if err := decodeJSON(r, &in); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		set, err := cutruleset.Replace(r.Context(), db, DefaultTenantID, r.PathValue("id"), in)
		if err != nil {
			writeCutRuleSetError(w, err)
			return
		}
		mat.Trigger()
		writeJSON(w, http.StatusOK, set)
	}
}

func deleteCutRuleSet(db storage.DB, mat *materializer.Materializer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := cutruleset.Delete(r.Context(), db, DefaultTenantID, r.PathValue("id")); err != nil {
			handleDBError(w, err)
			return
		}
		mat.Trigger()
		w.WriteHeader(http.StatusNoContent)
	}
}

func previewCutRuleSet() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in cutruleset.PreviewInput
		if err := decodeJSON(r, &in); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		res, err := cutruleset.Preview(in)
		if err != nil {
			writeCutRuleSetError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, res)
	}
}
