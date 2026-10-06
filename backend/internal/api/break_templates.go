package api

import (
	"net/http"

	"github.com/rafaelvieiras/playheadtv/backend/internal/breaktemplate"
	"github.com/rafaelvieiras/playheadtv/backend/internal/materializer"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

// registerBreakTemplates mounts the break template resource: an ordered list of
// steps that replaces the single item a preroll, midroll or postroll filler
// rule inserts at a program boundary (docs/ARCHITECTURE.md "Break templates").
// Every write retriggers the materializer, like a filler rule does.
func registerBreakTemplates(mux Router, db storage.DB, mat *materializer.Materializer) {
	mux.HandleFunc("POST /break-templates", createBreakTemplate(db, mat))
	mux.HandleFunc("GET /break-templates", listBreakTemplates(db))
	mux.HandleFunc("GET /break-templates/{id}", getBreakTemplate(db))
	mux.HandleFunc("PUT /break-templates/{id}", updateBreakTemplate(db, mat))
	mux.HandleFunc("DELETE /break-templates/{id}", deleteBreakTemplate(db, mat))
}

// writeBreakTemplateError maps a store error to a response: bad input is a 400
// with the specific message, anything else follows handleDBError.
func writeBreakTemplateError(w http.ResponseWriter, err error) {
	if breaktemplate.IsValidation(err) {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	handleDBError(w, err)
}

func createBreakTemplate(db storage.DB, mat *materializer.Materializer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in breaktemplate.Input
		if err := decodeJSON(r, &in); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		t, err := breaktemplate.Create(r.Context(), db, DefaultTenantID, in)
		if err != nil {
			writeBreakTemplateError(w, err)
			return
		}
		mat.Trigger()
		writeJSON(w, http.StatusCreated, t)
	}
}

func listBreakTemplates(db storage.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		templates, err := breaktemplate.List(r.Context(), db, DefaultTenantID)
		if err != nil {
			handleDBError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, templates)
	}
}

func getBreakTemplate(db storage.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		t, err := breaktemplate.Get(r.Context(), db, DefaultTenantID, r.PathValue("id"))
		if err != nil {
			handleDBError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, t)
	}
}

func updateBreakTemplate(db storage.DB, mat *materializer.Materializer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in breaktemplate.Input
		if err := decodeJSON(r, &in); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		t, err := breaktemplate.Replace(r.Context(), db, DefaultTenantID, r.PathValue("id"), in)
		if err != nil {
			writeBreakTemplateError(w, err)
			return
		}
		mat.Trigger()
		writeJSON(w, http.StatusOK, t)
	}
}

func deleteBreakTemplate(db storage.DB, mat *materializer.Materializer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := breaktemplate.Delete(r.Context(), db, DefaultTenantID, r.PathValue("id")); err != nil {
			handleDBError(w, err)
			return
		}
		mat.Trigger()
		w.WriteHeader(http.StatusNoContent)
	}
}
