package server

import (
	"net/http"
	"strings"

	"go.xchunk.org/vela/api"
	"go.xchunk.org/vela/internal/service"
)

func (s *Server) handleDirList(w http.ResponseWriter, r *http.Request, p service.Principal) error {
	q := r.URL.Query()
	recursive := true
	var err error
	if q.Has("recursive") {
		recursive, err = boolParam(q.Get("recursive"), "recursive")
		if err != nil {
			return err
		}
	}
	orphaned, err := boolParam(q.Get("orphaned"), "orphaned")
	if err != nil {
		return err
	}
	limit, err := intParam(q.Get("limit"), "limit")
	if err != nil {
		return err
	}
	out, err := s.svc.ListDirPolicies(r.Context(), p, service.DirPolicyParams{Prefix: q.Get("prefix"), Recursive: recursive, Orphaned: orphaned, Limit: limit, Cursor: q.Get("cursor")})
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func dirWriteOptions(r *http.Request) (service.DirWriteOptions, error) {
	rev, err := ifMatchHeader(r)
	if err != nil {
		return service.DirWriteOptions{}, err
	}
	out := service.DirWriteOptions{IfMatch: rev}
	if value, ok := headerValue(r.Header, "If-None-Match"); ok {
		if strings.TrimSpace(value) != "*" {
			return out, requestError(service.ErrInvalidRequest, "If-None-Match only supports \"*\"")
		}
		out.IfNoneMatch = true
	}
	return out, nil
}

func writeDir(w http.ResponseWriter, status int, out api.EffectiveDir) {
	if out.Rule != nil {
		w.Header().Set("ETag", api.FormatRevisionETag(out.Rule.Revision))
	}
	writeJSON(w, status, out)
}

func (s *Server) handleDirShow(w http.ResponseWriter, r *http.Request, p service.Principal) error {
	out, err := s.svc.ShowDir(r.Context(), p, r.PathValue("prefix"))
	if err != nil {
		return err
	}
	writeDir(w, http.StatusOK, out)
	return nil
}

func (s *Server) handleDirPut(w http.ResponseWriter, r *http.Request, p service.Principal) error {
	opts, err := dirWriteOptions(r)
	if err != nil {
		return err
	}
	// Decode through the strict patch parser so null and non-object documents
	// cannot silently turn into an accidental full reset.
	var patch api.DirPolicyPatch
	if err := decodeJSON(w, r, smallBodyLimit, &patch); err != nil {
		return err
	}
	for _, null := range []bool{patch.Index.Null, patch.Hidden.Null, patch.NoIndex.Null, patch.Title.Null, patch.Description.Null} {
		if null {
			return requestError(service.ErrInvalidRequest, "PUT fields must not be null; use PATCH to reset fields")
		}
	}
	req := api.DirPolicyRequest{Index: patch.Index.Value, Hidden: patch.Hidden.Value, NoIndex: patch.NoIndex.Value, Title: patch.Title.Value, Description: patch.Description.Value}
	if patch.Index.Set && req.Index == "" || patch.NoIndex.Set && req.NoIndex == "" {
		return requestError(service.ErrInvalidRequest, "index and noindex must be on, off or inherit")
	}
	out, created, err := s.svc.PutDir(r.Context(), p, r.PathValue("prefix"), req, opts)
	if err != nil {
		return err
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
		w.Header().Set("Location", api.DirRoute(out.Prefix))
	}
	writeDir(w, status, out)
	return nil
}

func (s *Server) handleDirPatch(w http.ResponseWriter, r *http.Request, p service.Principal) error {
	opts, err := dirWriteOptions(r)
	if err != nil {
		return err
	}
	var patch api.DirPolicyPatch
	if err := decodeJSON(w, r, smallBodyLimit, &patch); err != nil {
		return err
	}
	out, err := s.svc.PatchDir(r.Context(), p, r.PathValue("prefix"), patch, opts)
	if err != nil {
		return err
	}
	writeDir(w, http.StatusOK, out)
	return nil
}

func (s *Server) handleDirDelete(w http.ResponseWriter, r *http.Request, p service.Principal) error {
	opts, err := dirWriteOptions(r)
	if err != nil {
		return err
	}
	if err := s.svc.DeleteDir(r.Context(), p, r.PathValue("prefix"), opts); err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
	return nil
}
