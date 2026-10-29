package control

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5"
)

// Keep the JSON array API while retaining only one database row and one JSON
// row buffer. A full-result encoder otherwise holds both all decoded rows and
// an equally large encoding buffer, multiplied by concurrent polling clients.
func (s *Server) sendRowsWith(w http.ResponseWriter, rows pgx.Rows, err error, transform func(map[string]any)) {
	if rows != nil {
		defer rows.Close()
	}
	if err != nil {
		s.dbError(w, err)
		return
	}
	if !rows.Next() {
		if err = rows.Err(); err != nil {
			s.dbError(w, err)
			return
		}
		writeJSON(w, 200, []any{})
		return
	}
	row, err := pgx.RowToMap(rows)
	if err != nil {
		s.dbError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if _, err = io.WriteString(w, "["); err != nil {
		return
	}
	encoder := json.NewEncoder(w)
	for {
		if transform != nil {
			transform(row)
		}
		if err = encoder.Encode(row); err != nil {
			return
		}
		if !rows.Next() {
			if err = rows.Err(); err != nil {
				// Leave the array incomplete so clients reject a partial snapshot.
				// Headers have already been sent; do not append an error object.
				slog.Error("streaming database response interrupted", "error", err)
				return
			}
			_, _ = io.WriteString(w, "]\n")
			return
		}
		row, err = pgx.RowToMap(rows)
		if err != nil {
			slog.Error("streaming database response interrupted", "error", err)
			return
		}
		if _, err = io.WriteString(w, ","); err != nil {
			return
		}
	}
}
