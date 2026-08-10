package api

import (
	"cmp"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/monbooru/monbooru/internal/models"
	"github.com/monbooru/monbooru/internal/tags"
)

type categoryResponse struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Color     string `json:"color"`
	IsBuiltin bool   `json:"is_builtin"`
}

func toCategoryResponse(c models.TagCategory) categoryResponse {
	return categoryResponse{ID: c.ID, Name: c.Name, Color: c.Color, IsBuiltin: c.IsBuiltin}
}

// writeCategoryError maps category-service errors to status codes. It
// differs from writeTagError on ErrCategoryNotFound: editing a missing
// category id is a 404 here, whereas referencing an unknown category
// for a tag op is a 400.
func writeCategoryError(w http.ResponseWriter, err error) {
	if writeSentinelError(w, err, []sentinelStatus{
		{tags.ErrCategoryNotFound, http.StatusNotFound, "not_found"},
		{tags.ErrCategoryExists, http.StatusConflict, "conflict"},
		{tags.ErrBuiltinCategory, http.StatusBadRequest, "invalid_request"},
		{tags.ErrBuiltinCategoryName, http.StatusBadRequest, "invalid_request"},
		{tags.ErrInvalidCategoryName, http.StatusBadRequest, "invalid_request"},
		{tags.ErrInvalidCategoryColor, http.StatusBadRequest, "invalid_request"},
		{tags.ErrReservedCategoryName, http.StatusBadRequest, "invalid_request"},
		{tags.ErrInvalidMoveTarget, http.StatusBadRequest, "invalid_request"},
		{tags.ErrRatingTagImmutable, http.StatusBadRequest, "invalid_request"},
	}) {
		return
	}
	var collision *tags.ErrCategoryMoveCollision
	if errors.As(err, &collision) {
		apiError(w, http.StatusConflict, "conflict", err.Error())
		return
	}
	apiError(w, http.StatusInternalServerError, "internal_error", err.Error())
}

// getCategory reads one category row for the post-mutation response.
func getCategory(g Gallery, id int64) (models.TagCategory, error) {
	var c models.TagCategory
	var isBuiltin int
	err := g.DB.Read.QueryRow(
		`SELECT id, name, color, is_builtin FROM tag_categories WHERE id = ?`, id,
	).Scan(&c.ID, &c.Name, &c.Color, &isBuiltin)
	if errors.Is(err, sql.ErrNoRows) {
		return c, tags.ErrCategoryNotFound
	}
	c.IsBuiltin = isBuiltin == 1
	return c, err
}

// listCategories handles GET /api/v1/categories.
func (h *Handler) listCategories(w http.ResponseWriter, r *http.Request) {
	g, ok := h.resolveGallery(w, r)
	if !ok {
		return
	}
	cats, err := g.TagSvc.ListCategories()
	if serverError(w, err) {
		return
	}
	out := make([]categoryResponse, 0, len(cats))
	for _, c := range cats {
		out = append(out, toCategoryResponse(c))
	}
	writeJSON(w, http.StatusOK, out)
}

// createCategory handles POST /api/v1/categories. color defaults to the
// neutral grey the web form uses when blank.
func (h *Handler) createCategory(w http.ResponseWriter, r *http.Request) {
	g, ok := h.resolveGallery(w, r)
	if !ok {
		return
	}
	var body struct {
		Name  string `json:"name"`
		Color string `json:"color"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	color := strings.TrimSpace(body.Color)
	color = cmp.Or(color, "#888888")
	cat, err := g.TagSvc.CreateCategory(body.Name, color)
	if err != nil {
		writeCategoryError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toCategoryResponse(*cat))
}

// patchCategory handles PATCH /api/v1/categories/{id}: rename and/or
// recolor. The colour format is validated before any write so a bad
// colour can't leave a half-applied rename behind. Built-in categories
// accept a recolor but refuse a rename.
func (h *Handler) patchCategory(w http.ResponseWriter, r *http.Request) {
	g, id, ok := h.galleryAndID(w, r)
	if !ok {
		return
	}
	var body struct {
		Name  *string `json:"name"`
		Color *string `json:"color"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Name == nil && body.Color == nil {
		apiError(w, http.StatusBadRequest, "invalid_request", "name or color is required")
		return
	}
	if body.Color != nil && !tags.IsValidCategoryColor(strings.TrimSpace(*body.Color)) {
		apiError(w, http.StatusBadRequest, "invalid_request", tags.ErrInvalidCategoryColor.Error())
		return
	}
	if body.Name != nil {
		if err := g.TagSvc.RenameCategory(id, *body.Name); err != nil {
			writeCategoryError(w, err)
			return
		}
	}
	if body.Color != nil {
		if err := g.TagSvc.UpdateCategoryColor(id, strings.TrimSpace(*body.Color)); err != nil {
			writeCategoryError(w, err)
			return
		}
	}
	cat, err := getCategory(g, id)
	if err != nil {
		writeCategoryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toCategoryResponse(cat))
}

// deleteCategory handles DELETE /api/v1/categories/{id}. action is
// "move" (default; reparent the category's tags to target_id, or
// general when target_id is omitted) or "delete_all" (drop the tags
// too). Built-in categories cannot be deleted.
func (h *Handler) deleteCategory(w http.ResponseWriter, r *http.Request) {
	g, id, ok := h.galleryAndID(w, r)
	if !ok {
		return
	}
	var body struct {
		Action   string `json:"action"`
		TargetID int64  `json:"target_id"`
	}
	// An empty body is valid: it means the default move-to-general, so
	// EOF is not an error - only malformed JSON is.
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		apiError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}
	action := body.Action
	action = cmp.Or(action, "move")
	if action != "move" && action != "delete_all" {
		apiError(w, http.StatusBadRequest, "invalid_request", "action must be 'move' or 'delete_all'")
		return
	}
	if err := g.TagSvc.DeleteCategoryMoveOrDelete(id, action, body.TargetID); err != nil {
		writeCategoryError(w, err)
		return
	}
	g.invalidate()
	w.WriteHeader(http.StatusNoContent)
}
